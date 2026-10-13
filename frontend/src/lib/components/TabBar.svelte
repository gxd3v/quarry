<script lang="ts">
  import { connections } from '../app/connections.svelte'
  import { menu } from '../app/menu.svelte'
  import { notify } from '../app/notify.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import Icon from '../ui/Icon.svelte'

  function close(t: Tab) {
    tabs.close(t).catch((err: unknown) => notify.error(err, 'Close tab'))
  }

  function onauxclick(e: MouseEvent, t: Tab) {
    if (e.button === 1) {
      e.preventDefault()
      close(t)
    }
  }

  function oncontextmenu(e: MouseEvent, t: Tab) {
    menu.open(e, [
      { label: 'Close', hint: 'Ctrl+W', run: () => close(t) },
      { label: 'Close others', run: () => tabs.tabs.filter((x) => x !== t).forEach(close) },
      'separator',
      { label: 'Change connection…', run: () => ((tabs.activeKey = t.key), (ui.scoping = true)) },
    ])
  }
</script>

<div class="tabbar">
  <div class="tabs" role="tablist">
    {#each tabs.tabs as t (t.key)}
      {@const env = connections.env(t.connId)}
      <div
        class="tab"
        class:active={t.key === tabs.activeKey}
        role="tab"
        tabindex="0"
        aria-selected={t.key === tabs.activeKey}
        data-env={env}
        title="{t.title} @ {connections.name(t.connId)}"
        onmousedown={(e) => {
          if (e.button === 0) tabs.activate(t)
          else if (e.button === 1) e.preventDefault()
        }}
        onkeydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') tabs.activate(t)
        }}
        onauxclick={(e) => onauxclick(e, t)}
        oncontextmenu={(e) => oncontextmenu(e, t)}
      >
        <span class="env-dot" data-env={env}></span>
        <span class="title">{t.title}</span>
        <span class="conn">@{connections.name(t.connId)}</span>
        {#if t.busy}<span class="spinner small"></span>{/if}
        {#if t.tx.inTx}<span class="tx" title="Open transaction">●</span>{/if}
        <button
          class="close"
          type="button"
          tabindex="-1"
          title="Close (Ctrl+W)"
          onmousedown={(e) => e.stopPropagation()}
          onclick={() => close(t)}><Icon name="close" size={11} /></button
        >
      </div>
    {/each}
  </div>
  <button class="icon-btn new" type="button" title="New script (Ctrl+T)" onclick={() => (ui.picking = true)}>
    <Icon name="plus" />
  </button>
</div>

<style>
  .tabbar {
    display: flex;
    align-items: stretch;
    height: 34px;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
  }

  .tabs {
    display: flex;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }

  .tab {
    position: relative;
    display: flex;
    align-items: center;
    gap: 6px;
    max-width: 260px;
    padding: 0 6px 0 12px;
    color: var(--overlay1);
    border-right: 1px solid var(--surface0);
    cursor: pointer;
    white-space: nowrap;
    user-select: none;
    outline: none;
  }

  .tab:hover {
    color: var(--subtext1);
    background: color-mix(in srgb, var(--mantle) 60%, transparent);
  }

  .tab.active {
    color: var(--text);
    background: var(--base);
  }

  .tab.active::before {
    content: '';
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 2px;
    background: var(--env);
  }

  .tab:focus-visible {
    box-shadow: inset 0 0 0 1px var(--accent);
  }

  .title {
    font-weight: 500;
  }

  .conn {
    color: var(--overlay0);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .tab.active .conn {
    color: var(--subtext0);
  }

  .tx {
    color: var(--peach);
    font-size: 10px;
  }

  .spinner.small {
    width: 10px;
    height: 10px;
  }

  .close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    padding: 0;
    background: none;
    border: none;
    border-radius: 4px;
    color: var(--overlay0);
    cursor: pointer;
    opacity: 0;
  }

  .tab:hover .close,
  .tab.active .close {
    opacity: 1;
  }

  .close:hover {
    background: var(--surface1);
    color: var(--text);
  }

  .new {
    align-self: center;
    margin: 0 6px;
  }
</style>
