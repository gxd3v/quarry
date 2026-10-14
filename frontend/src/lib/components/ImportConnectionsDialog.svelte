<script lang="ts">
  import { connectionClient } from '../api/clients'
  import { errorMessage } from '../api/errors'
  import { openFileDialog } from '../api/server'
  import { connections } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import type { DiscoveredConnection } from '../gen/quarry/v1/connection_pb'
  import Modal from '../ui/Modal.svelte'

  let { onclose }: { onclose: () => void } = $props()

  let found = $state.raw<DiscoveredConnection[]>([])
  let errors = $state.raw<string[]>([])
  let picked = $state<boolean[]>([])
  let paths = $state<string[]>([])
  let busy = $state<'' | 'scan' | 'save'>('scan')
  let error = $state('')

  const count = $derived(picked.filter(Boolean).length)

  async function scan() {
    busy = 'scan'
    error = ''
    try {
      const res = await connectionClient().discoverConnections({ paths })
      found = res.connections
      errors = res.errors
      picked = res.connections.map(() => true)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  $effect(() => {
    void scan()
  })

  async function addFile() {
    try {
      const p = await openFileDialog('Read connections from', 'pgpass, pg_service.conf, tnsnames.ora', '*.conf;*.ora;*pgpass*')
      if (!p) return
      if (!paths.includes(p)) paths = [...paths, p]
      await scan()
    } catch (err) {
      error = errorMessage(err)
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    busy = 'save'
    error = ''
    let n = 0
    try {
      for (const [i, d] of found.entries()) {
        if (!picked[i] || !d.config) continue
        await connections.save(d.config, { password: d.password, forgetPassword: false })
        n++
      }
      notify.flash(`Imported ${n} ${n === 1 ? 'connection' : 'connections'}`)
      onclose()
    } catch (err) {
      error = `${n ? `Imported ${n}, then: ` : ''}${errorMessage(err)}`
      await scan()
    } finally {
      busy = ''
    }
  }

  function target(d: DiscoveredConnection): string {
    const c = d.config!
    const sid = c.options.sid ? ` (SID ${c.options.sid})` : ''
    return `${c.user ? `${c.user}@` : ''}${c.host}:${c.port}${c.database ? `/${c.database}` : ''}${sid}`
  }

  const base = (p: string) => p.split(/[\\/]/).pop() ?? p
</script>

<Modal title="Import connections" width={720} {onclose}>
  <form id="import-conns" onsubmit={save}>
    <p class="intro">Connections found in libpq (<code>.pgpass</code>, <code>pg_service.conf</code>) and Oracle (<code>tnsnames.ora</code>) files that are not saved yet.</p>
    {#if busy === 'scan' && !found.length}
      <p class="muted"><span class="spinner"></span> Looking for files…</p>
    {:else if !found.length}
      <p class="muted">No new connections found.</p>
    {:else}
      <div class="list" role="list">
        <label class="row head">
          <input type="checkbox" checked={count === found.length} onchange={(e) => (picked = found.map(() => e.currentTarget.checked))} />
          <span>Name</span><span>Type</span><span>Server</span><span>From</span>
        </label>
        {#each found as d, i (i)}
          <label class="row" role="listitem">
            <input type="checkbox" bind:checked={picked[i]} />
            <span class="name">{d.config?.name}{#if d.password !== undefined}<span class="pw" title="Password from the password file">🔑</span>{/if}</span>
            <span class="kind">{d.config?.kind}</span>
            <span class="target" title={target(d)}>{target(d)}</span>
            <span class="src" title={d.source}>{base(d.source)}</span>
          </label>
        {/each}
      </div>
    {/if}
    {#if errors.length}
      <ul class="warn">
        {#each errors as e (e)}<li>{e}</li>{/each}
      </ul>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
  </form>

  {#snippet footer()}
    <button class="btn" type="button" disabled={busy !== ''} onclick={addFile}>Add file…</button>
    <span class="spacer"></span>
    <button class="btn" type="button" onclick={onclose}>Cancel</button>
    <button class="btn primary" type="submit" form="import-conns" disabled={busy !== '' || count === 0}>
      {#if busy === 'save'}<span class="spinner"></span>{/if}Import {count || ''}
    </button>
  {/snippet}
</Modal>

<style>
  .intro {
    margin: 0 0 10px;
    color: var(--text-2);
  }

  .muted {
    color: var(--text-2);
  }

  .list {
    max-height: 360px;
    overflow: auto;
    border: 1px solid var(--line);
    border-radius: 6px;
  }

  .row {
    display: grid;
    grid-template-columns: 24px 1.2fr 70px 2fr 1fr;
    align-items: center;
    gap: 8px;
    padding: 4px 8px;
    border-bottom: 1px solid var(--line);
    cursor: pointer;
  }

  .row.head {
    position: sticky;
    top: 0;
    background: var(--bg-side);
    color: var(--text-2);
    font-size: 12px;
  }

  .row > span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .target,
  .src {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--subtext1);
  }

  .pw {
    margin-left: 6px;
    font-size: 11px;
  }

  .row input {
    accent-color: var(--accent);
  }

  .warn {
    margin: 10px 0 0;
    padding-left: 18px;
    color: var(--yellow);
  }

  .error {
    margin: 10px 0 0;
    padding: 6px 10px;
    border-radius: var(--radius);
    color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
  }

  .spacer {
    flex: 1;
  }
</style>
