<script lang="ts">
  import { listen } from '../app/actions'
  import type { Tab } from '../app/tabs.svelte'
  import { prettyJson } from '../viewer/format'

  let { tab }: { tab: Tab } = $props()
  const l = $derived(tab.listener!)

  const time = (d: Date) => d.toLocaleTimeString(undefined, { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0')
  let list = $state<HTMLElement>()

  // Follow new notifications while scrolled to the bottom.
  $effect(() => {
    void l.items.length
    const el = list
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 40) queueMicrotask(() => (el.scrollTop = el.scrollHeight))
  })
</script>

<div class="notifications">
  <div class="head">
    <span class="state {l.state}">
      {#if l.state === 'starting'}<span class="spinner"></span> Subscribing…{:else if l.state === 'listening'}● Listening{:else if l.state === 'stopped'}Stopped{:else}Failed{/if}
    </span>
    <span class="channels">{l.channels.join(', ')}</span>
    <span class="count">{l.items.length.toLocaleString('en-US')}{l.dropped ? ` (+${l.dropped.toLocaleString('en-US')} dropped)` : ''}</span>
    <span class="spacer"></span>
    <button class="btn small" type="button" onclick={() => l.clear()} disabled={!l.items.length}>Clear</button>
    {#if l.state === 'listening' || l.state === 'starting'}
      <button class="btn small" type="button" onclick={() => l.stop()}>Stop</button>
    {:else}
      <button class="btn small" type="button" onclick={() => listen(tab)}>Listen…</button>
    {/if}
  </div>
  {#if l.error}<p class="error">{l.error}</p>{/if}
  <div class="list" bind:this={list}>
    {#each l.items as n, i (i)}
      {@const pretty = prettyJson(n.payload)}
      <div class="item">
        <span class="at">{time(n.at)}</span>
        <span class="ch">{n.channel}</span>
        <span class="pid" title="Sending server process">{n.pid}</span>
        <pre class="payload">{pretty ?? (n.payload || '(no payload)')}</pre>
      </div>
    {:else}
      <p class="empty">{l.state === 'listening' ? `Waiting for NOTIFY on ${l.channels.join(', ')}…` : ''}</p>
    {/each}
  </div>
</div>

<style>
  .notifications {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }

  .head {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 5px 10px;
    border-bottom: 1px solid var(--border);
  }

  .state.listening {
    color: var(--green);
  }

  .state.failed {
    color: var(--red);
  }

  .state.stopped,
  .count {
    color: var(--text-2);
  }

  .channels {
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .spacer {
    flex: 1;
  }

  .list {
    flex: 1;
    overflow: auto;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .item {
    display: grid;
    grid-template-columns: 100px 140px 70px minmax(0, 1fr);
    gap: 10px;
    padding: 3px 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
  }

  .at,
  .pid {
    color: var(--muted);
  }

  .ch {
    color: var(--sapphire);
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .payload {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    font: inherit;
  }

  .empty {
    padding: 10px;
    color: var(--text-2);
    font-family: var(--font-ui);
  }

  .error {
    margin: 8px 10px 0;
    color: var(--red);
  }
</style>
