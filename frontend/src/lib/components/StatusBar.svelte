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

<footer class="status" data-env={cfg?.env || 'local'}>
  {#if tab && cfg}
    <span class="env-dot" data-env={cfg.env || 'local'}></span>
    <span class="name">{cfg.name}</span>
    <span class="env">{cfg.env || 'local'}</span>
    <span class="state {stateLabel(state)}" title={status?.error || undefined}>
      {stateLabel(state)}{#if state === ConnectionState.CONNECTED && status && status.latencyMs > 0n}{` · ${status.latencyMs} ms`}{/if}{#if status?.error && state !== ConnectionState.CONNECTED}{` · ${status.error}`}{/if}
    </span>
  {:else}
    <span class="muted">No script open</span>
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
    gap: 8px;
    height: 24px;
    padding: 0 10px;
    background: var(--crust);
    border-top: 1px solid var(--surface0);
    color: var(--subtext0);
    font-size: 12px;
    white-space: nowrap;
    overflow: hidden;
  }

  .status[data-env='prod'] {
    border-top-color: var(--env-prod);
  }

  .name {
    color: var(--text);
  }

  .update {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 0 8px;
    height: 18px;
    border: 1px solid color-mix(in srgb, var(--accent) 50%, transparent);
    border-radius: 9px;
    background: color-mix(in srgb, var(--accent) 10%, transparent);
    color: var(--text);
    font-size: 11px;
    cursor: pointer;
  }

  .update:hover {
    background: color-mix(in srgb, var(--accent) 20%, transparent);
  }

  .update .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
  }

  .env {
    color: var(--env);
  }

  .state {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .state.connected {
    color: var(--green);
  }

  .state.lost {
    color: var(--peach);
  }

  .state.failed {
    color: var(--red);
  }

  .state.disconnected,
  .muted {
    color: var(--overlay0);
  }

  .flash {
    margin-left: auto;
    color: var(--subtext1);
  }

  .task {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 4px 0 8px;
    height: 18px;
    background: var(--surface0);
    border-radius: 9px;
    font-variant-numeric: tabular-nums;
  }

  .task .label {
    max-width: 160px;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text);
  }

  .task .rows {
    color: var(--green);
  }

  .spinner.small {
    width: 10px;
    height: 10px;
  }

  .cancel {
    display: inline-flex;
    padding: 2px;
    background: none;
    border: none;
    border-radius: 50%;
    color: var(--overlay1);
    cursor: pointer;
  }

  .cancel:hover {
    color: var(--red);
    background: var(--surface1);
  }

  .path {
    max-width: 40%;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--overlay0);
    direction: rtl;
  }
</style>
