<script lang="ts">
  import { connectionClient } from '../api/clients'
  import { errorMessage } from '../api/errors'
  import { deleteConnection } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import {
    applyParsed,
    configFromForm,
    emptyForm,
    ENVS,
    formFromConfig,
    passwordForSave,
    passwordForTest,
    SSL_MODES,
    validate,
    type ConnForm,
  } from '../connections/form'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    // Connection to edit, '' for a new one.
    id: string
    onclose: () => void
  }

  let { id, onclose }: Props = $props()

  function initial(): ConnForm {
    const c = connections.get(id)
    if (c?.config) return formFromConfig(c.config, c.hasPassword)
    return emptyForm(connections.drivers[0]?.kind ?? 'postgres')
  }

  let form = $state(initial())
  let uri = $state('')
  let error = $state('')
  let test = $state<{ ok: boolean; text: string } | null>(null)
  let busy = $state<'' | 'test' | 'save' | 'parse'>('')

  const driver = $derived(connections.drivers.find((d) => d.kind === form.kind))
  const kinds = $derived(connections.drivers.length ? connections.drivers.map((d) => d.kind) : ['postgres'])
  const groups = $derived([...new Set(connections.list.map((c) => c.config!.group).filter(Boolean))].sort())
  const editing = $derived(id !== '')

  async function parse() {
    if (!uri.trim()) return
    busy = 'parse'
    error = ''
    try {
      form = applyParsed(form, await connectionClient().parseURI({ uri: uri.trim() }))
      uri = ''
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function runTest() {
    const problem = validate({ ...form, name: form.name || 'test' })
    if (problem) {
      error = problem
      return
    }
    busy = 'test'
    error = ''
    test = null
    try {
      const res = await connectionClient().testConnection({ config: configFromForm(form), password: passwordForTest(form) })
      test = { ok: true, text: `Connected in ${res.latencyMs} ms` }
    } catch (err) {
      test = { ok: false, text: errorMessage(err) }
    } finally {
      busy = ''
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    const problem = validate(form)
    if (problem) {
      error = problem
      return
    }
    busy = 'save'
    error = ''
    try {
      const saved = await connections.save(configFromForm(form), passwordForSave(form))
      notify.flash(`Saved ${saved.config!.name}`)
      onclose()
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function remove() {
    if (await deleteConnection(id)) onclose()
  }

  function option(key: string, value: string) {
    form.options = { ...form.options, [key]: value }
  }
</script>

<Modal title={editing ? `Edit ${form.name || 'connection'}` : 'New connection'} width={560} env={form.env} {onclose}>
  <form id="conn-form" onsubmit={save} autocomplete="off">
    <div class="uri">
      <input
        class="field"
        type="text"
        placeholder="Paste a URI, e.g. postgres://user:pass@host:5432/db?sslmode=disable"
        bind:value={uri}
        onpaste={() => setTimeout(parse)}
        onkeydown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            parse()
          }
        }}
      />
      <button class="btn" type="button" disabled={!uri.trim() || busy !== ''} onclick={parse}>Fill</button>
    </div>

    <div class="grid">
      <label class="wide">
        <span>Name</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="field" type="text" bind:value={form.name} autofocus />
      </label>

      <label>
        <span>Type</span>
        <select
          class="field"
          bind:value={form.kind}
          disabled={kinds.length < 2}
        >
          {#each kinds as k (k)}<option value={k}>{k}</option>{/each}
        </select>
      </label>

      <div class="env-field">
        <span>Environment</span>
        <div class="envs" role="radiogroup" aria-label="Environment">
          {#each ENVS as env (env)}
            <button
              type="button"
              role="radio"
              aria-checked={form.env === env}
              class="env"
              class:on={form.env === env}
              data-env={env}
              onclick={() => (form.env = env)}
            >
              <span class="env-dot" data-env={env}></span>{env}
            </button>
          {/each}
        </div>
      </div>

      <label class="host">
        <span>Host</span>
        <input class="field" type="text" bind:value={form.host} />
      </label>
      <label>
        <span>Port</span>
        <input class="field" type="text" inputmode="numeric" placeholder={String(driver?.defaultPort ?? '')} bind:value={form.port} />
      </label>

      <label>
        <span>User</span>
        <input class="field" type="text" bind:value={form.user} />
      </label>
      <label>
        <span>Database</span>
        <input class="field" type="text" bind:value={form.database} />
      </label>

      <label>
        <span>Password</span>
        <input
          class="field"
          type="password"
          disabled={form.passwordMode === 'ask'}
          placeholder={form.passwordMode === 'ask' ? 'Asked on connect' : form.hasPassword && !form.passwordTouched ? '•••••• (saved)' : ''}
          bind:value={form.password}
          oninput={() => (form.passwordTouched = true)}
        />
      </label>
      <label>
        <span>Password storage</span>
        <select class="field" bind:value={form.passwordMode}>
          <option value="keyring">Save in keyring</option>
          <option value="ask">Ask every time</option>
        </select>
      </label>

      <label>
        <span>Group</span>
        <input class="field" type="text" list="conn-groups" bind:value={form.group} />
        <datalist id="conn-groups">
          {#each groups as g (g)}<option value={g}></option>{/each}
        </datalist>
      </label>
      <label>
        <span>SSL mode</span>
        <select class="field" value={form.options.sslmode ?? ''} onchange={(e) => option('sslmode', e.currentTarget.value)}>
          {#each SSL_MODES as m (m)}<option value={m}>{m || 'default (prefer)'}</option>{/each}
        </select>
      </label>

      <div class="checks wide">
        <label class="check"><input type="checkbox" bind:checked={form.readOnly} /> Read-only</label>
        <label class="check"><input type="checkbox" bind:checked={form.favorite} /> Favorite</label>
      </div>
    </div>

    {#if test}
      <p class="test" class:ok={test.ok}>{test.text}</p>
    {/if}
    {#if error}
      <p class="error">{error}</p>
    {/if}
  </form>

  {#snippet footer()}
    {#if editing}
      <button class="btn danger" type="button" onclick={remove}>Delete</button>
    {/if}
    <button class="btn" type="button" disabled={busy !== ''} onclick={runTest}>
      {#if busy === 'test'}<span class="spinner"></span>{/if}Test
    </button>
    <span class="spacer"></span>
    <button class="btn" type="button" onclick={onclose}>Cancel</button>
    <button class="btn primary" type="submit" form="conn-form" disabled={busy !== ''}>Save</button>
  {/snippet}
</Modal>

<style>
  .uri {
    display: flex;
    gap: 6px;
    margin-bottom: 14px;
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 12px;
  }

  label,
  .env-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  label > span,
  .env-field > span {
    color: var(--subtext0);
    font-size: 12px;
  }

  .wide {
    grid-column: 1 / -1;
  }

  .envs {
    display: flex;
    gap: 4px;
  }

  .env {
    flex: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 5px;
    height: 28px;
    padding: 0 4px;
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 5px;
    color: var(--subtext0);
    cursor: pointer;
  }

  .env.on {
    border-color: var(--env);
    color: var(--env);
    background: color-mix(in srgb, var(--env) 12%, var(--crust));
    font-weight: 600;
  }

  .checks {
    display: flex;
    gap: 20px;
  }

  .check {
    flex-direction: row;
    align-items: center;
    gap: 6px;
    cursor: pointer;
  }

  .check input {
    accent-color: var(--accent);
  }

  .test,
  .error {
    margin: 12px 0 0;
    padding: 6px 10px;
    border-radius: 5px;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .test {
    color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
  }

  .test.ok {
    color: var(--green);
    background: color-mix(in srgb, var(--green) 10%, transparent);
  }

  .error {
    color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
  }

  .spacer {
    flex: 1;
  }
</style>
