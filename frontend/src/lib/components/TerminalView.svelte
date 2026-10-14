<script lang="ts">
  import { connections } from '../app/connections.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import Icon from '../ui/Icon.svelte'

  let { tab }: { tab: Tab } = $props()
  const term = $derived(tab.terminal!)
  let host = $state<HTMLDivElement>()

  // The xterm element lives in the session; it is moved here while shown.
  $effect(() => {
    const h = host
    const t = term
    if (!h) return
    h.appendChild(t.element)
    const ro = new ResizeObserver(() => t.fit())
    ro.observe(h)
    t.fit()
    t.term.focus()
    return () => {
      ro.disconnect()
      t.element.remove()
    }
  })

  export function focus() {
    term.term.focus()
  }
</script>

<div class="terminal" data-env={connections.env(tab.connId)}>
  <div class="bar">
    <span class="env-dot" data-env={connections.env(tab.connId)}></span>
    <span class="name">{connections.name(tab.connId)}</span>
    {#if tab.scope.database || tab.scope.schema}<span class="scope">{[tab.scope.database, tab.scope.schema].filter(Boolean).join(' › ')}</span>{/if}
    <code class="cmd" title={term.display}>{term.display}</code>
    <span class="spacer"></span>
    {#if term.state === 'starting'}
      <span class="state"><span class="spinner"></span>Starting…</span>
    {:else if term.state === 'exited' || term.state === 'failed'}
      <span class="state" class:failed={term.state === 'failed'}>{term.status}</span>
      <button class="btn" type="button" onclick={() => void term.start()}><Icon name="refresh" size={12} />Restart</button>
    {/if}
  </div>
  <div class="host" bind:this={host}></div>
</div>

<style>
  .terminal {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    height: 100%;
    background: var(--crust);
  }

  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 10px;
    border-bottom: 1px solid var(--border);
    background: var(--mantle);
    min-width: 0;
  }

  .name {
    font-weight: 600;
  }

  .scope,
  .state {
    color: var(--subtext0);
  }

  .state.failed {
    color: var(--red);
  }

  .cmd {
    color: var(--muted);
    font-family: var(--font-mono);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  .spacer {
    flex: 1;
  }

  .host {
    padding: 4px 0 0 6px;
    min-height: 0;
    overflow: hidden;
  }

  .host :global(.xterm-host) {
    height: 100%;
  }
</style>
