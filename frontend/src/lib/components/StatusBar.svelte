<script lang="ts">
  import { connections, ConnectionState, stateLabel } from '../app/connections.svelte'
  import { exportTasks } from '../app/exports.svelte'
  import Icon from '../ui/Icon.svelte'
  import { notify } from '../app/notify.svelte'
  import { tabs } from '../app/tabs.svelte'
  import { updates } from '../app/updates.svelte'

  const tab = $derived(tabs.active)
  const cfg = $derived(tab ? connections.config(tab.connId) : undefined)
  const status = $derived(tab ? connections.status(tab.connId) : undefined)
  const state = $derived(status?.state ?? ConnectionState.DISCONNECTED)

  function compact(n: number): string {
    if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`
    if (n >= 1e4) return `${Math.round(n / 1e3)}k`
    return Math.round(n).toLocaleString('en-US')
  }
</script>

<footer class="status">
  {#if tab && cfg}
    <span class="state {stateLabel(state)}" title={status?.error || undefined}>
      <span class="dot"></span>{stateLabel(state)}{#if state === ConnectionState.CONNECTED && status && status.latencyMs > 0n}<span class="muted">{` · ${status.latencyMs} ms`}</span>{/if}{#if status?.error && state !== ConnectionState.CONNECTED}{` · ${status.error}`}{/if}
    </span>
  {/if}
  <span class="flash">{notify.status}</span>
  {#each exportTasks.tasks as task (task.id)}
    <span class="task" data-state={task.state} title="Export of {task.job.label} to {task.job.path}">
      {#if task.state === 'running'}<span class="spinner small"></span>{:else}<Icon name="export" size={11} />{/if}
      <span class="label">{task.job.label}</span>
      {#if task.state === 'queued'}
        <span class="muted">queued</span>
      {:else}
        <span class="rows">{task.rows.toLocaleString('en-US')} rows</span>
        <span class="muted">{compact(task.rate)}/s</span>
      {/if}
      <button class="cancel" type="button" title="Cancel export" onclick={() => exportTasks.cancel(task)}><Icon name="close" size={10} /></button>
    </span>
  {/each}
  {#if tab?.path}<span class="path" title={tab.path}>{tab.path}</span>{/if}
  {#if updates.available}
    <button class="update" type="button" title="Release notes and update" onclick={() => (updates.open = true)}>
      <span class="dot"></span>Update {updates.info?.latest}
    </button>
  {/if}
</footer>

<style>
  .status {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 26px;
    padding: 0 12px;
    background: var(--bg-chrome);
    color: var(--text-3);
    font-size: 12px;
    white-space: nowrap;
    overflow: hidden;
  }

  .state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    overflow: hidden;
    text-overflow: ellipsis;
    text-transform: capitalize;
  }

  .state .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--overlay0);
  }

  .state.connected .dot {
    background: var(--green);
  }

  .state.connecting .dot,
  .state.lost .dot {
    background: var(--peach);
  }

  .state.failed {
    color: var(--red);
  }

  .state.failed .dot {
    background: var(--red);
  }

  .muted {
    color: var(--text-3);
    text-transform: none;
  }

  .flash {
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-2);
  }

  .task {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 20px;
    padding: 0 4px 0 8px;
    border-radius: 10px;
    background: var(--bg-hover);
    color: var(--text-2);
  }

  .task[data-state='failed'] {
    color: var(--red);
  }

  .task .label {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .rows {
    font-variant-numeric: tabular-nums;
  }

  .cancel {
    display: inline-flex;
    padding: 2px;
    background: none;
    border: none;
    border-radius: 50%;
    color: var(--text-3);
    cursor: pointer;
  }

  .cancel:hover {
    color: var(--text-1);
    background: var(--bg-active);
  }

  .spinner.small {
    width: 10px;
    height: 10px;
  }

  .path {
    margin-left: auto;
    max-width: 40%;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-3);
    direction: rtl;
  }

  .update {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 18px;
    padding: 0 8px;
    border: none;
    border-radius: 9px;
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    color: var(--accent);
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
  }

  .path + .update {
    margin-left: 0;
  }

  .update:not(.path + .update) {
    margin-left: auto;
  }

  .update:hover {
    background: color-mix(in srgb, var(--accent) 26%, transparent);
  }

  .update .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
  }
</style>
