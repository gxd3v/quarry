<script lang="ts">
  import { onMount } from 'svelte'
  import { canImport, closeTab, exportTableData, guard, importCsv, listen, pickAndNewScript, pickAndOpenTerminal, run, saveTab } from '../app/actions'
  import { onBeforeClose } from '../api/server'
  import { connections } from '../app/connections.svelte'
  import { exportTasks } from '../app/exports.svelte'
  import { clampLayout } from '../app/layout'
  import { menu } from '../app/menu.svelte'
  import { notify } from '../app/notify.svelte'
  import { settings } from '../app/settings.svelte'
  import * as table from '../app/table.svelte'
  import { editorHost, HISTORY, tabs } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { workspace } from '../app/workspace.svelte'
  import { COPY_FORMATS } from '../grid/copy'
  import { chordOf, keymap, label, TERMINAL_ACTIONS } from '../keymap'
  import { clipboard } from '../ui/clipboard'
  import ContextMenu from '../ui/ContextMenu.svelte'
  import { startDrag } from '../ui/drag'
  import CellEditor from './CellEditor.svelte'
  import CellViewer from './CellViewer.svelte'
  import CommandPalette from './CommandPalette.svelte'
  import ConnectionDialog from './ConnectionDialog.svelte'
  import ConnectionPicker from './ConnectionPicker.svelte'
  import ContextBar from './ContextBar.svelte'
  import ExportDialog from './ExportDialog.svelte'
  import Explorer from './Explorer.svelte'
  import GotoObject from './GotoObject.svelte'
  import ImportConnectionsDialog from './ImportConnectionsDialog.svelte'
  import ImportCsvDialog from './ImportCsvDialog.svelte'
  import PromptDialogs from './PromptDialogs.svelte'
  import ResultsPanel from './ResultsPanel.svelte'
  import ScopePicker from './ScopePicker.svelte'
  import ScriptEditor from './ScriptEditor.svelte'
  import ShortcutsDialog from './ShortcutsDialog.svelte'
  import StatusBar from './StatusBar.svelte'
  import TableView from './TableView.svelte'
  import TabBar from './TabBar.svelte'
  import TerminalView from './TerminalView.svelte'
  import Toasts from './Toasts.svelte'

  let center = $state<HTMLElement>()
  let explorerRef = $state<ReturnType<typeof Explorer>>()
  let resultsRef = $state<ReturnType<typeof ResultsPanel>>()
  let tableRef = $state<ReturnType<typeof TableView>>()
  let terminalRef = $state<ReturnType<typeof TerminalView>>()

  const layout = $derived(ui.layout)

  function dragExplorer(e: PointerEvent) {
    const w0 = layout.explorer
    startDrag(e, (dx) => (ui.layout = clampLayout({ ...ui.layout, explorer: w0 + dx })))
  }

  function dragEditor(e: PointerEvent) {
    const h = center?.getBoundingClientRect().height ?? 1
    const f0 = layout.editor
    startDrag(e, (_, dy) => (ui.layout = clampLayout({ ...ui.layout, editor: f0 + dy / h })))
  }

  function resizeKey(e: KeyboardEvent, which: 'explorer' | 'editor') {
    const dir = e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : 0
    if (!dir) return
    e.preventDefault()
    const l = ui.layout
    ui.layout = clampLayout(which === 'explorer' ? { ...l, explorer: l.explorer + dir * 20 } : { ...l, editor: l.editor + dir * 0.03 })
  }

  // Global shortcuts go first, unless a modal dialog or menu owns the keyboard
  // (or Escape should close the completion popup).
  function onkeydown(e: KeyboardEvent) {
    if (document.querySelector('dialog[open]') || menu.current) return
    if (e.key === 'Escape' && document.querySelector('.cm-tooltip-autocomplete')) return
    // A focused component may own some chords (e.g. Ctrl+Shift+N sets NULL in a table grid).
    const own = e.target instanceof Element ? e.target.closest('[data-keys]')?.getAttribute('data-keys') : null
    if (own && own.split(' ').includes(chordOf(e))) return
    if (e.target instanceof Element && e.target.closest('.xterm-host')) {
      const id = keymap.boundTo(chordOf(e))
      if (!id || !TERMINAL_ACTIONS.has(id)) return
    }
    if (keymap.handle(e)) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  // withTab runs f on the active script or table tab.
  const withTab = (f: (t: NonNullable<typeof tabs.active>) => unknown) => () => {
    const t = tabs.active
    if (!t || t.kind === 'terminal') return false
    f(t)
  }

  const withTable = (f: (t: NonNullable<typeof tabs.active>) => unknown) => () => {
    const t = tabs.active
    if (t?.kind !== 'table') return false
    f(t)
  }

  // The grid of the active tab's shown result, or of its table data.
  const shownGrid = () => (tabs.active?.kind === 'table' ? (tableRef?.shownGrid() ?? null) : (resultsRef?.shownGrid() ?? null))

  const applyTable = (t: NonNullable<typeof tabs.active>) => guard('Apply changes', () => table.applyChanges(t, tableRef?.scrollPos() ?? null))

  onMount(() => {
    const off = keymap.register(
      { id: 'tab.new', category: 'Tab', title: 'New script…', run: () => void pickAndNewScript() },
      { id: 'tab.close', category: 'Tab', title: 'Close tab', run: withTab(closeTab) },
      { id: 'tab.next', category: 'Tab', title: 'Next tab', run: () => tabs.step(1) },
      { id: 'tab.prev', category: 'Tab', title: 'Previous tab', run: () => tabs.step(-1) },
      {
        id: 'tab.changeConnection',
        category: 'Tab',
        title: 'Change connection or scope…',
        run: () => {
          if (tabs.active?.kind !== 'script') return false
          ui.scoping = true
        },
      },
      { id: 'file.newScratch', category: 'File', title: 'New scratch', run: () => guard('New scratch', () => workspace.newScratch()) },
      { id: 'file.open', category: 'File', title: 'Open file…', run: () => guard('Open file', () => workspace.openFile()) },
      { id: 'file.save', category: 'File', title: 'Save (Apply in a table)', run: withTab((t) => (t.kind === 'table' ? applyTable(t) : saveTab(t))) },
      {
        id: 'file.saveAs',
        category: 'File',
        title: 'Save as…',
        run: () => {
          const t = tabs.active
          if (t?.kind !== 'script') return false
          saveTab(t, true)
        },
      },
      { id: 'run.statement', category: 'Run', title: 'Run statement or selection', run: withTab((t) => run(t, t.kind === 'script' ? tabs.target(t, false) : undefined)) },
      { id: 'run.script', category: 'Run', title: 'Run script', run: withTab((t) => run(t, t.kind === 'script' ? tabs.target(t, true) : undefined)) },
      {
        id: 'run.listen',
        category: 'Run',
        title: 'Listen to notifications (LISTEN)…',
        run: withTab((t) => (t.kind === 'script' ? void listen(t) : false)),
      },
      {
        id: 'run.explain',
        category: 'Run',
        title: 'Explain statement',
        run: withTab((t) => (t.kind === 'script' ? void tabs.explain(t, tabs.target(t, false), false) : false)),
      },
      {
        id: 'run.explainAnalyze',
        category: 'Run',
        title: 'Explain analyze statement (runs it, then rolls back)',
        run: withTab((t) => (t.kind === 'script' ? void tabs.explain(t, tabs.target(t, false), true) : false)),
      },
      { id: 'table.refresh', category: 'Table', title: 'Refresh data', run: withTable((t) => run(t)) },
      { id: 'table.apply', category: 'Table', title: 'Apply pending changes', run: withTable(applyTable) },
      { id: 'table.discard', category: 'Table', title: 'Discard pending changes', run: withTable((t) => t.table && table.discardChanges(t.table)) },
      { id: 'table.export', category: 'Table', title: 'Export data…', run: withTable(exportTableData) },
      {
        id: 'table.importCsv',
        category: 'Table',
        title: 'Import CSV…',
        run: withTable((t) => (t.table && canImport(t.connId, t.table.node.kind) ? importCsv(t.connId, t.table.node) : false)),
      },
      {
        id: 'results.export',
        category: 'Results',
        title: 'Export result…',
        run: () => {
          const t = tabs.active
          if (!t) return false
          if (t.kind === 'table') exportTableData(t)
          else if (!resultsRef?.exportShown()) return false
        },
      },
      {
        id: 'run.cancel',
        category: 'Run',
        title: 'Cancel run',
        run: () => {
          const t = tabs.active
          if (!t?.busy) return false
          void tabs.cancel(t)
        },
      },
      { id: 'connection.new', category: 'Connection', title: 'New connection…', run: () => void (ui.editing = '') },
      { id: 'connection.import', category: 'Connection', title: 'Import connections (pgpass, pg_service, tnsnames)…', run: () => void (ui.importingConnections = true) },
      { id: 'terminal.open', category: 'Connection', title: 'Open terminal', run: () => void pickAndOpenTerminal() },
      { id: 'palette.commands', category: 'View', title: 'Command palette', run: () => void (ui.palette = 'commands') },
      { id: 'palette.objects', category: 'Go', title: 'Go to object…', run: withTab(() => (ui.palette = 'objects')) },
      { id: 'keymap.edit', category: 'Preferences', title: 'Keyboard shortcuts', run: () => void (ui.shortcuts = true) },
      {
        id: 'results.history',
        category: 'View',
        title: 'Query history',
        run: withTab((t) => {
          t.view = HISTORY
          setTimeout(() => resultsRef?.focus(), 0)
        }),
      },
      {
        id: 'results.viewCell',
        category: 'Results',
        title: 'View selected cell',
        run: () => {
          const g = shownGrid()
          const at = g?.grid.active()
          if (!g || !at) return false
          g.open(at.row, at.col)
        },
      },
      ...COPY_FORMATS.map((f) => ({
        id: `results.copy.${f.format}`,
        category: 'Results',
        title: `Copy selection as ${f.label}`,
        run: () => {
          const g = shownGrid()
          if (!g?.grid.active()) return false
          void g.grid.copy(f.format)
        },
      })),
      { id: 'focus.explorer', category: 'View', title: 'Focus explorer', run: () => explorerRef?.focus() },
      {
        id: 'focus.editor',
        category: 'View',
        title: 'Focus editor',
        run: () => {
          if (tabs.active?.kind === 'table') return void tableRef?.focus()
          if (tabs.active?.kind === 'terminal') return void terminalRef?.focus()
          if (!editorHost.view) return false
          editorHost.view.focus()
        },
      },
      {
        id: 'focus.results',
        category: 'View',
        title: 'Focus results',
        run: () => (tabs.active?.kind === 'table' ? tableRef?.focus() : resultsRef?.focus()),
      },
    )
    window.addEventListener('keydown', onkeydown, true)
    // Dev builds expose the stores to tooling (smoke tests over CDP).
    if (import.meta.env.DEV) {
      Object.assign(window, {
        __quarry: { tabs, editorHost, connections, keymap, workspace, settings, ui, clipboard, shownGrid, table, exportTasks, tableView: () => tableRef },
      })
    }

    const flush = () => void workspace.flush()
    const hidden = () => {
      if (document.visibilityState === 'hidden') flush()
    }
    window.addEventListener('beforeunload', flush)
    document.addEventListener('visibilitychange', hidden)
    const offClose = onBeforeClose(() => workspace.flush())

    const watch = new AbortController()
    settings.load().catch((err: unknown) => notify.error(err, 'Load settings'))
    connections
      .load()
      .then(() => workspace.restore())
      .catch(() => {})
    void connections.watch(watch.signal)
    return () => {
      off()
      window.removeEventListener('keydown', onkeydown, true)
      window.removeEventListener('beforeunload', flush)
      document.removeEventListener('visibilitychange', hidden)
      offClose()
      watch.abort()
    }
  })

  // Save the session whenever what it records changes; edits and cursor
  // moves schedule it from the editor.
  $effect(() => {
    void tabs.activeKey
    for (const t of tabs.tabs) void [t.connId, t.scope.database, t.scope.schema, t.path, t.title, t.dirty, t.table?.filter, t.table?.order, t.table?.view]
    void [layout.explorer, layout.editor, layout.scratches]
    workspace.schedule()
  })

  const hint = (id: string) => {
    void settings.keys
    return label(keymap.keysFor(id)[0] ?? '')
  }
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
      {#if tab.kind !== 'terminal'}<ContextBar {tab} />{/if}
      {#if tab.kind === 'terminal'}
        <div class="center table" bind:this={center}>
          {#key tab.key}<TerminalView {tab} bind:this={terminalRef} />{/key}
        </div>
      {:else if tab.kind === 'table'}
        <div class="center table" bind:this={center}>
          {#key tab.key}<TableView {tab} bind:this={tableRef} />{/key}
        </div>
      {:else}
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
      {/if}
    {:else}
      <div class="welcome" bind:this={center}>
        <h1>Quarry</h1>
        <p>Every script runs on exactly one connection. Pick one to start.</p>
        <div class="actions">
          <button class="btn primary" type="button" onclick={() => pickAndNewScript()}>New script <span class="kbd">{hint('tab.new')}</span></button>
          <button class="btn" type="button" onclick={() => guard('Open file', () => workspace.openFile())}>Open file <span class="kbd">{hint('file.open')}</span></button>
          <button class="btn" type="button" onclick={() => (ui.editing = '')}>New connection <span class="kbd">{hint('connection.new')}</span></button>
        </div>
        <dl>
          <dt>{hint('run.statement')}</dt><dd>Run the statement at the cursor or the selection</dd>
          <dt>{hint('run.script')}</dt><dd>Run the whole script</dd>
          <dt>{hint('file.newScratch')} · {hint('file.save')}</dt><dd>New scratch, save</dd>
          <dt>{hint('palette.commands')}</dt><dd>Command palette</dd>
          <dt>{hint('palette.objects')}</dt><dd>Go to a table, view or function</dd>
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
{#if ui.pick}
  <ConnectionPicker request={ui.pick} onnew={() => (ui.editing = '')} />
{/if}
{#if ui.scoping && tabs.active}
  <ScopePicker tab={tabs.active} onclose={() => (ui.scoping = false)} />
{/if}
{#if ui.palette === 'commands'}
  <CommandPalette onclose={() => (ui.palette = null)} />
{:else if ui.palette === 'objects' && tabs.active}
  <GotoObject tab={tabs.active} onclose={() => (ui.palette = null)} />
{/if}
{#if ui.shortcuts}
  <ShortcutsDialog onclose={() => (ui.shortcuts = false)} />
{/if}
{#if ui.cell}
  <CellViewer cell={ui.cell} onclose={() => (ui.cell = null)} />
{/if}
{#if ui.cellEdit}
  <CellEditor request={ui.cellEdit} onclose={() => (ui.cellEdit = null)} />
{/if}
{#if ui.importingConnections}
  <ImportConnectionsDialog onclose={() => (ui.importingConnections = false)} />
{/if}
{#if ui.importingCsv}
  <ImportCsvDialog request={ui.importingCsv} onclose={() => (ui.importingCsv = null)} />
{/if}
{#if ui.exporting}
  <ExportDialog request={ui.exporting} onclose={() => (ui.exporting = null)} />
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

  .center.table {
    display: block;
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
