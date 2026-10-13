<script lang="ts">
  import { connections, ConnectionState, stateLabel } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import { tabs } from '../app/tabs.svelte'

  const tab = $derived(tabs.active)
  const cfg = $derived(tab ? connections.config(tab.connId) : undefined)
  const status = $derived(tab ? connections.status(tab.connId) : undefined)
  const state = $derived(status?.state ?? ConnectionState.DISCONNECTED)
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
</style>
