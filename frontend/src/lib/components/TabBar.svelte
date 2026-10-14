<script lang="ts">
  import { closeTab as close, exportTableData, pickAndNewScript, run, saveTab } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { menu } from '../app/menu.svelte'
  import { settings } from '../app/settings.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { keymap, label } from '../keymap'
  import Icon from '../ui/Icon.svelte'

  const hint = (id: string) => {
    void settings.keys
    return label(keymap.keysFor(id)[0] ?? '')
  }

  function onauxclick(e: MouseEvent, t: Tab) {
    if (e.button === 1) {
      e.preventDefault()
      close(t)
    }
  }

  function oncontextmenu(e: MouseEvent, t: Tab) {
    if (t.kind === 'terminal') {
      menu.open(e, [
        { label: 'Restart', icon: 'refresh', run: () => void t.terminal?.start() },
        'separator',
        { label: 'Close', hint: hint('tab.close'), run: () => close(t) },
        { label: 'Close others', run: () => tabs.tabs.filter((x) => x !== t).forEach(close) },
      ])
      return
    }
    if (t.kind === 'table') {
      menu.open(e, [
        { label: 'Refresh', icon: 'refresh', run: () => void run(t) },
        { label: 'Export data…', icon: 'export', run: () => exportTableData(t) },
        'separator',
        { label: 'Close', hint: hint('tab.close'), run: () => close(t) },
        { label: 'Close others', run: () => tabs.tabs.filter((x) => x !== t).forEach(close) },
      ])
      return
    }
    menu.open(e, [
      { label: 'Save', icon: 'save', hint: hint('file.save'), run: () => saveTab(t) },
      { label: 'Save as…', hint: hint('file.saveAs'), run: () => saveTab(t, true) },
      'separator',
      { label: 'Close', hint: hint('tab.close'), run: () => close(t) },
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
        title="{t.table ? t.table.node.path.join(' › ') : t.path || t.title} @ {connections.name(t.connId)}{t.dirty ? ' (unsaved)' : ''}{t.table?.changes ? ' (pending changes)' : ''}"
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
        {#if t.scratch}<span class="scratch" title="Scratch"><Icon name="scratch" size={12} /></span>{/if}
        {#if t.terminal}<span class="tkind" title="Terminal"><Icon name="terminal" size={12} /></span>{/if}
        {#if t.table}<span class="tkind" title={t.table.node.kind}><Icon name={t.table.node.kind === 'view' ? 'view' : 'table'} size={12} /></span>{/if}
        <span class="title">{t.title}</span>
        <span class="conn">@{connections.name(t.connId)}</span>
        {#if t.busy}<span class="spinner small"></span>{/if}
        {#if t.tx.inTx}<span class="tx" title="Open transaction">●</span>{/if}
        <button
          class="close"
          class:dirty={t.dirty || !!t.table?.changes}
          type="button"
          tabindex="-1"
          title="Close ({hint('tab.close')})"
          onmousedown={(e) => e.stopPropagation()}
          onclick={() => close(t)}
          ><span class="dot" aria-label="Unsaved changes">●</span><Icon name="close" size={11} /></button
        >
      </div>
    {/each}
  </div>
  <button class="icon-btn new" type="button" title="New script ({hint('tab.new')})" onclick={() => pickAndNewScript()}>
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
  .tab.active .close,
  .close.dirty {
    opacity: 1;
  }

  .close .dot {
    display: none;
    color: var(--peach);
    font-size: 10px;
  }

  .close.dirty:not(:hover) .dot {
    display: inline;
  }

  .close.dirty:not(:hover) :global(.icon) {
    display: none;
  }

  .scratch {
    color: var(--overlay1);
  }

  .tkind {
    color: var(--blue);
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
