<script lang="ts">
  import { openURL } from '../api/server'
  import { updates } from '../app/updates.svelte'
  import { blocks } from '../markdown/blocks'
  import Modal from '../ui/Modal.svelte'

  let { onclose }: { onclose: () => void } = $props()

  const info = $derived(updates.info!)
  const notes = $derived(blocks(info.notes))
  const date = $derived(info.date ? new Date(info.date).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }) : '')
  const managed = $derived(info.install === 'managed')
  const percent = $derived(updates.progress && updates.progress.total > 0 ? Math.round((updates.progress.done / updates.progress.total) * 100) : null)
  const mb = (n: number) => (n / 1048576).toFixed(1)

  function close() {
    if (!updates.installing) onclose()
  }
</script>

<Modal title="Quarry {info.latest}" width={560} onclose={close}>
  <p class="meta">
    {date ? `Released ${date} · ` : ''}Installed: {info.current}
  </p>
  <div class="notes">
    {#each notes as b, i (i)}
      {#if b.kind === 'heading'}
        <h4>{#each b.inline as s, j (j)}{@render span(s)}{/each}</h4>
      {:else if b.kind === 'list'}
        <ul>
          {#each b.items as item, j (j)}<li>{#each item as s, k (k)}{@render span(s)}{/each}</li>{/each}
        </ul>
      {:else}
        <p>{#each b.inline as s, j (j)}{@render span(s)}{/each}</p>
      {/if}
    {:else}
      <p class="muted">No release notes.</p>
    {/each}
  </div>

  {#if updates.installing}
    <div class="progress">
      <div class="bar"><div class="fill" style:width="{percent ?? 100}%" class:busy={percent === null}></div></div>
      <span>
        {#if updates.progress && percent !== null && percent < 100}Downloading {mb(updates.progress.done)} of {mb(updates.progress.total)} MB{:else}Installing and restarting…{/if}
      </span>
    </div>
  {/if}
  {#if managed}<p class="muted">This copy is managed by a package manager, which installs the update.</p>{/if}
  {#if updates.error}<p class="error">Update failed: {updates.error}</p>{/if}

  {#snippet footer()}
    <button class="btn" type="button" onclick={() => openURL(info.url)}>Release page</button>
    <span class="spacer"></span>
    <button class="btn" type="button" disabled={updates.installing} onclick={close}>Later</button>
    {#if !managed}
      <button class="btn primary" type="button" disabled={updates.installing} onclick={() => updates.install()}>
        {#if updates.installing}<span class="spinner"></span>{/if}Update and restart
      </button>
    {/if}
  {/snippet}
</Modal>

{#snippet span(s: import('../markdown/blocks').Inline)}
  {#if s.kind === 'bold'}<b>{s.text}</b>{:else if s.kind === 'code'}<code>{s.text}</code>{:else if s.kind === 'link'}<a
      href={s.url}
      onclick={(e) => {
        e.preventDefault()
        openURL(s.url)
      }}>{s.text}</a
    >{:else}{s.text}{/if}
{/snippet}

<style>
  .meta {
    margin: 0 0 10px;
    color: var(--text-2);
    font-size: 12px;
  }

  .notes {
    max-height: 320px;
    overflow: auto;
    padding: 8px 12px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--bg-side);
    line-height: 1.5;
  }

  .notes h4 {
    margin: 8px 0 4px;
    font-size: 13px;
  }

  .notes p,
  .notes ul {
    margin: 4px 0;
  }

  .notes ul {
    padding-left: 18px;
  }

  .notes code {
    font-family: var(--font-mono);
    font-size: 12px;
    padding: 0 3px;
    border-radius: 3px;
    background: var(--surface0);
  }

  .notes a {
    color: var(--blue);
  }

  .muted {
    color: var(--text-2);
  }

  .progress {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    color: var(--text-2);
  }

  .bar {
    flex: 1;
    height: 6px;
    border-radius: 3px;
    background: var(--surface0);
    overflow: hidden;
  }

  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.2s;
  }

  .fill.busy {
    opacity: 0.6;
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
