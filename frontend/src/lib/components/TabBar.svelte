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
        title="{t.table ? t.table.node.path.join(' › ') : t.path || t.title} · {connections.name(t.connId)}{t.dirty ? ' · unsaved' : ''}{t.table?.changes ? ' · pending changes' : ''}"
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
        <span class="kind">
          {#if t.terminal}<Icon name="terminal" size={13} />{:else if t.table}<Icon name={t.table.node.kind === 'view' ? 'view' : t.table.node.kind === 'key' ? 'key' : 'table'} size={13} />{:else if t.scratch}<Icon name="scratch" size={13} />{:else}<Icon name="script" size={13} />{/if}
          <span class="env-mark"></span>
        </span>
        <span class="title">{t.title}</span>
        {#if t.busy}<span class="spinner small"></span>{:else if t.tx.inTx}<span class="tx" title="Open transaction"></span>{/if}
        <button
          class="close"
          class:dirty={t.dirty || !!t.table?.changes}
          type="button"
          tabindex="-1"
          title="Close ({hint('tab.close')})"
          onmousedown={(e) => e.stopPropagation()}
          onclick={() => close(t)}
          ><span class="dot" aria-label="Unsaved changes"></span><Icon name="close" size={10} /></button
        >
      </div>
    {/each}
  </div>
  <button class="icon-btn new" type="button" title="New script ({hint('tab.new')})" onclick={() => pickAndNewScript()}>
    <Icon name="plus" size={14} />
  </button>
</div>

<style>
  .tabbar {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    min-width: 0;
    height: 100%;
  }

  .tabs {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    min-width: 0;
    height: 100%;
    overflow-x: auto;
    scrollbar-width: none;
  }

  .tabs::-webkit-scrollbar {
    display: none;
  }

  .tab {
    --wails-draggable: no-drag;
    position: relative;
    display: flex;
    align-items: center;
    gap: 7px;
    flex: none;
    max-width: 220px;
    height: 32px;
    padding: 0 6px 0 10px;
    border-radius: 8px 8px 0 0;
    color: var(--text-3);
    cursor: default;
    white-space: nowrap;
    user-select: none;
    outline: none;
    transition:
      background 0.12s,
      color 0.12s;
  }

  .tab:hover {
    color: var(--text-2);
    background: var(--bg-hover);
  }

  .tab.active {
    color: var(--text-1);
    background: var(--bg-main);
  }

  /* Rounded joins between the active tab and the content below. */
  .tab.active::before,
  .tab.active::after {
    content: '';
    position: absolute;
    bottom: 0;
    width: 8px;
    height: 8px;
    background: transparent;
    pointer-events: none;
  }

  .tab.active::before {
    left: -8px;
    border-bottom-right-radius: 8px;
    box-shadow: 4px 4px 0 4px var(--bg-main);
  }

  .tab.active::after {
    right: -8px;
    border-bottom-left-radius: 8px;
    box-shadow: -4px 4px 0 4px var(--bg-main);
  }

  .tab:focus-visible {
    box-shadow: inset var(--ring);
  }

  .kind {
    position: relative;
    display: inline-flex;
    color: var(--text-3);
  }

  .tab.active .kind {
    color: var(--env);
  }

  /* The environment shows as a dot on the icon. */
  .env-mark {
    position: absolute;
    right: -3px;
    bottom: -2px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--env);
    box-shadow: 0 0 0 2px var(--bg-chrome);
  }

  .tab:hover .env-mark {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--text) 6%, var(--bg-chrome));
  }

  .tab.active .env-mark {
    box-shadow: 0 0 0 2px var(--bg-main);
  }

  .title {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .tx {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--peach);
  }

  .spinner.small {
    width: 10px;
    height: 10px;
  }

  .close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: 18px;
    height: 18px;
    padding: 0;
    background: none;
    border: none;
    border-radius: var(--radius-s);
    color: var(--text-3);
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
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--text-2);
  }

  .close.dirty:not(:hover) .dot {
    display: block;
  }

  .close.dirty:not(:hover) :global(.icon) {
    display: none;
  }

  .close:hover {
    background: var(--bg-active);
    color: var(--text-1);
  }

  .new {
    --wails-draggable: no-drag;
    align-self: center;
    margin-left: 2px;
  }
</style>
