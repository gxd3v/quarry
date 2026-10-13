<script lang="ts">
  import { onMount } from 'svelte'
  import { run } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { loadLayout, saveLayout, clampLayout } from '../app/layout'
  import { menu } from '../app/menu.svelte'
  import { notify } from '../app/notify.svelte'
  import { tabs } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { editorHost } from '../app/tabs.svelte'
  import { keymap, label } from '../keymap'
  import ContextMenu from '../ui/ContextMenu.svelte'
  import { startDrag } from '../ui/drag'
  import ConnectionDialog from './ConnectionDialog.svelte'
  import ConnectionPicker from './ConnectionPicker.svelte'
  import ContextBar from './ContextBar.svelte'
  import Explorer from './Explorer.svelte'
  import PromptDialogs from './PromptDialogs.svelte'
  import ResultsPanel from './ResultsPanel.svelte'
  import ScopePicker from './ScopePicker.svelte'
  import ScriptEditor from './ScriptEditor.svelte'
  import StatusBar from './StatusBar.svelte'
  import TabBar from './TabBar.svelte'
  import Toasts from './Toasts.svelte'

  let layout = $state(loadLayout())
  let center = $state<HTMLElement>()
  let explorerRef = $state<ReturnType<typeof Explorer>>()
  let resultsRef = $state<ReturnType<typeof ResultsPanel>>()

  function dragExplorer(e: PointerEvent) {
    const w0 = layout.explorer
    startDrag(
      e,
      (dx) => (layout = clampLayout({ ...layout, explorer: w0 + dx })),
      () => saveLayout(layout),
    )
  }

  function dragEditor(e: PointerEvent) {
    const h = center?.getBoundingClientRect().height ?? 1
    const f0 = layout.editor
    startDrag(
      e,
      (_, dy) => (layout = clampLayout({ ...layout, editor: f0 + dy / h })),
      () => saveLayout(layout),
    )
  }

  function resizeKey(e: KeyboardEvent, which: 'explorer' | 'editor') {
    const dir = e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : 0
    if (!dir) return
    e.preventDefault()
    layout = clampLayout(which === 'explorer' ? { ...layout, explorer: layout.explorer + dir * 20 } : { ...layout, editor: layout.editor + dir * 0.03 })
    saveLayout(layout)
  }

  // Global shortcuts go first, unless a modal dialog or menu owns the keyboard.
  function onkeydown(e: KeyboardEvent) {
    if (document.querySelector('dialog[open]') || menu.current) return
    if (keymap.handle(e)) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  onMount(() => {
    const off = keymap.register(
      { id: 'tab.new', title: 'New script', run: () => void (ui.picking = true) },
      {
        id: 'tab.close',
        title: 'Close tab',
        run: () => {
          if (!tabs.active) return false
          tabs.close(tabs.active).catch((err: unknown) => notify.error(err, 'Close tab'))
        },
      },
      { id: 'tab.next', title: 'Next tab', run: () => tabs.step(1) },
      { id: 'tab.prev', title: 'Previous tab', run: () => tabs.step(-1) },
      {
        id: 'run.statement',
        title: 'Run statement or selection',
        run: () => {
          if (!tabs.active) return false
          void run(tabs.active, tabs.target(tabs.active, false))
        },
      },
      {
        id: 'run.script',
        title: 'Run script',
        run: () => {
          if (!tabs.active) return false
          void run(tabs.active, tabs.target(tabs.active, true))
        },
      },
      {
        id: 'run.cancel',
        title: 'Cancel run',
        run: () => {
          const t = tabs.active
          if (!t?.busy) return false
          void tabs.cancel(t)
        },
      },
      { id: 'connection.new', title: 'New connection', run: () => void (ui.editing = '') },
      { id: 'focus.explorer', title: 'Focus explorer', run: () => explorerRef?.focus() },
      {
        id: 'focus.editor',
        title: 'Focus editor',
        run: () => {
          if (!editorHost.view) return false
          editorHost.view.focus()
        },
      },
      { id: 'focus.results', title: 'Focus results', run: () => resultsRef?.focus() },
    )
    window.addEventListener('keydown', onkeydown, true)
    // Dev builds expose the stores to tooling (smoke tests over CDP).
    if (import.meta.env.DEV) Object.assign(window, { __quarry: { tabs, editorHost, connections, keymap } })

    const watch = new AbortController()
    connections.load().catch(() => {})
    void connections.watch(watch.signal)
    return () => {
      off()
      window.removeEventListener('keydown', onkeydown, true)
      watch.abort()
    }
  })

  const hint = (id: string) => label(keymap.keysFor(id)[0] ?? '')
</script>

<div class="shell" style:grid-template-columns="{layout.explorer}px 5px minmax(0, 1fr)">
  <div class="left">
    <Explorer bind:this={explorerRef} />
  </div>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (a focusable window splitter) -->
  <div
    class="split vertical"
    role="separator"
    aria-orientation="vertical"
    aria-label="Resize explorer"
    aria-valuenow={layout.explorer}
    tabindex="0"
    onpointerdown={dragExplorer}
    onkeydown={(e) => resizeKey(e, 'explorer')}
  ></div>

  <div class="main">
    <TabBar />
    {#if tabs.active}
      {@const tab = tabs.active}
      <ContextBar {tab} />
      <div class="center" bind:this={center} style:grid-template-rows="{layout.editor * 100}% 5px minmax(0, 1fr)">
        <div class="pane"><ScriptEditor {tab} /></div>
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (a focusable window splitter) -->
        <div
          class="split horizontal"
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize editor"
          aria-valuenow={Math.round(layout.editor * 100)}
          tabindex="0"
          onpointerdown={dragEditor}
          onkeydown={(e) => resizeKey(e, 'editor')}
        ></div>
        <div class="pane"><ResultsPanel {tab} bind:this={resultsRef} /></div>
      </div>
    {:else}
      <div class="welcome" bind:this={center}>
        <h1>Quarry</h1>
        <p>Every script runs on exactly one connection. Pick one to start.</p>
        <div class="actions">
          <button class="btn primary" type="button" onclick={() => (ui.picking = true)}>New script <span class="kbd">{hint('tab.new')}</span></button>
          <button class="btn" type="button" onclick={() => (ui.editing = '')}>New connection <span class="kbd">{hint('connection.new')}</span></button>
        </div>
        <dl>
          <dt>{hint('run.statement')}</dt><dd>Run the statement at the cursor or the selection</dd>
          <dt>{hint('run.script')}</dt><dd>Run the whole script</dd>
          <dt>Esc</dt><dd>Stop the running statement</dd>
          <dt>{hint('tab.close')} · {hint('tab.next')}</dt><dd>Close and switch tabs</dd>
          <dt>Alt+1 · Alt+2 · Alt+3</dt><dd>Focus explorer, editor, results</dd>
        </dl>
      </div>
    {/if}
  </div>

  <div class="bottom"><StatusBar /></div>
</div>

{#if ui.editing !== null}
  <ConnectionDialog id={ui.editing} onclose={() => (ui.editing = null)} />
{/if}
{#if ui.picking}
  <ConnectionPicker
    onclose={() => (ui.picking = false)}
    onnew={() => {
      ui.picking = false
      ui.editing = ''
    }}
  />
{/if}
{#if ui.scoping && tabs.active}
  <ScopePicker tab={tabs.active} onclose={() => (ui.scoping = false)} />
{/if}
<PromptDialogs />
<ContextMenu />
<Toasts />

<style>
  .shell {
    display: grid;
    grid-template-rows: minmax(0, 1fr) auto;
    height: 100%;
  }

  .left {
    min-width: 0;
    min-height: 0;
    border-right: 1px solid var(--surface0);
  }

  .main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }

  .bottom {
    grid-column: 1 / -1;
  }

  .center {
    flex: 1;
    min-height: 0;
    display: grid;
  }

  .pane {
    min-height: 0;
    overflow: hidden;
  }

  .split {
    background: var(--mantle);
    touch-action: none;
    outline: none;
  }

  .split.vertical {
    cursor: col-resize;
  }

  .split.horizontal {
    cursor: row-resize;
    border-block: 1px solid var(--surface0);
  }

  .split:hover,
  .split:focus-visible {
    background: var(--surface1);
  }

  :global(body.dragging) {
    user-select: none;
  }

  .welcome {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    color: var(--subtext0);
  }

  h1 {
    margin: 0;
    color: var(--accent);
    font-size: 28px;
    font-weight: 600;
    letter-spacing: 0.02em;
  }

  p {
    margin: 0;
  }

  .actions {
    display: flex;
    gap: 8px;
    margin: 8px 0 16px;
  }

  .actions .kbd {
    color: inherit;
    opacity: 0.7;
  }

  dl {
    display: grid;
    grid-template-columns: auto auto;
    gap: 6px 16px;
    margin: 0;
    font-size: 12px;
  }

  dt {
    text-align: right;
    font-family: var(--font-mono);
    color: var(--overlay1);
  }

  dd {
    margin: 0;
  }
</style>
