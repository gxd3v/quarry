<script lang="ts">
  import { onDestroy, tick, untrack } from 'svelte'
  import { canImport, guard, importCsv } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import type { MenuEntry } from '../app/menu.svelte'
  import {
    applyChanges,
    applyFilter,
    countRows,
    flavorOf,
    deleteRows,
    discardChanges,
    duplicateRows,
    filterHistory,
    insertRow,
    loadData,
    revertRows,
    setCell,
    setOrder,
  } from '../app/table.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import type { CopyOptions } from '../grid/copy'
  import Grid from '../grid/Grid.svelte'
  import DocumentsView from './DocumentsView.svelte'
  import { quoteIdent, schemaOfObject } from '../sql'
  import { cycleOrder, orderBy, sortOf } from '../table/order'
  import Icon from '../ui/Icon.svelte'

  interface Props {
    tab: Tab
    onexport: () => void
  }

  let { tab, onexport }: Props = $props()

  const OWN_KEYS = 'Mod+Shift+N Mod+D Delete Insert Alt+Insert'
  // Values longer than this, multi-line or JSON open in the cell editor.
  const INLINE_MAX = 300

  const t = $derived(tab.table!)

  const snap = $derived.by(() => {
    void tab.version
    const run = tab.run
    const r = run.results[0] ?? null
    return {
      result: r,
      rows: r?.buffer.length ?? 0,
      hasMore: r?.hasMore ?? false,
      truncated: r?.buffer.truncated ?? false,
      elapsedMs: r?.elapsedMs ?? 0,
      finished: run.finished,
      fetchable: run.fetchable,
      errors: run.messages.filter((m) => m.kind === 'error'),
    }
  })

  const source = $derived(snap.result ? t.source(snap.result.buffer) : null)
  const flavor = $derived(flavorOf(connections.config(tab.connId)?.kind ?? ''))
  // MongoDB results end with the whole document (see the mongo driver).
  const docCol = $derived(flavor === 'mongo' && source ? source.columns.findIndex((c) => c.name === '{document}') : -1)
  const showDocs = $derived(docCol >= 0 && t.documents)
  const FILTERS = {
    sql: { kw: 'WHERE', title: 'SQL WHERE condition; Enter applies', placeholder: "condition, e.g. status = 'open' (Enter)" },
    mongo: { kw: 'FILTER', title: 'MongoDB query document; Enter applies', placeholder: '{ status: "open", total: { $gt: 10 } } (Enter)' },
    redis: { kw: 'MATCH', title: 'Key pattern (folders, hashes, sets) or index range (lists, streams); Enter applies', placeholder: 'pattern, e.g. user:* (Enter)' },
  }
  const filterUI = $derived(FILTERS[flavor])
  let grid = $state<ReturnType<typeof Grid>>()
  let history = $state<string[]>([])

  // Load once when first shown.
  $effect(() => {
    if (untrack(() => !t.loaded)) void loadData(tab, { force: true })
  })

  $effect(() => {
    history = filterHistory(tab.connId, t.node)
    void t.filter
  })

  // Quick filter: narrow loaded rows once they stop arriving.
  $effect(() => {
    const q = t.quick.trim()
    const src = source
    void snap.rows
    if (!src || (!q && !src.index)) return
    const timer = setTimeout(() => {
      src.index = q ? src.buffer.filter(q) : null
      t.epoch++
    }, 150)
    return () => clearTimeout(timer)
  })

  // Restore the scroll position after a reload that asked for it.
  $effect(() => {
    if (!snap.finished || !t.restore || !grid) return
    const p = t.restore
    t.restore = null
    void tick().then(() => grid?.scrollTo(p))
  })

  // Switching tabs remounts the view; come back to the same place.
  onDestroy(() => {
    if (grid && !tab.busy) t.restore = grid.scrollPos()
  })

  export function shownGrid() {
    return grid && source ? { grid, open: openCell } : null
  }

  export function scrollPos() {
    return grid?.scrollPos() ?? null
  }

  export function focus() {
    grid?.focus()
  }

  // --- editing ---

  function colType(col: number): string {
    return source?.columns[col]?.type ?? ''
  }

  function needsEditor(v: string | null | undefined, col: number): boolean {
    if (v === null || v === undefined) return false
    return v.includes('\n') || v.includes('\r') || v.length > INLINE_MAX || source?.columns[col]?.kind === 'json' || /^jsonb?$/i.test(colType(col))
  }

  function openEditor(row: number, col: number, readonly = !t.editable) {
    const src = source
    if (!src) return
    const v = src.value(row, col)
    ui.cellEdit = {
      title: `${src.columns[col].name} · ${src.state(row) === 'new' ? 'new row' : `row ${row + 1}`}`,
      type: colType(col),
      value: v === undefined ? '' : v,
      readonly: readonly || src.state(row) === 'deleted',
      onsave: (value) => setCell(t, row, col, value),
    }
  }

  function openCell(row: number, col: number) {
    const src = source
    const ref = src?.ref(row)
    if (!src || !ref) return
    if (ref.kind === 'old' && !src.edited(row, col)) ui.cell = { buffer: src.buffer, row: ref.index, col }
    else openEditor(row, col)
  }

  function beginEdit(row: number, col: number): string | null {
    const src = source
    if (!src || src.state(row) === 'deleted') return null
    const v = src.value(row, col)
    if (needsEditor(v, col)) {
      openEditor(row, col)
      return null
    }
    return v ?? ''
  }

  function selectedRows(): number[] {
    const r = grid?.selection()
    if (!r) return []
    return Array.from({ length: r.r1 - r.r0 + 1 }, (_, i) => r.r0 + i)
  }

  // toggleNull sets the selected cells to NULL, or to '' when all are NULL.
  function toggleNull() {
    const r = grid?.selection()
    const src = source
    if (!r || !src) return
    let allNull = true
    for (let row = r.r0; row <= r.r1; row++) for (let c = r.c0; c <= r.c1; c++) if (src.value(row, c) !== null) allNull = false
    for (let row = r.r0; row <= r.r1; row++) for (let c = r.c0; c <= r.c1; c++) setCell(t, row, c, allNull ? '' : null)
  }

  async function addRow() {
    if (!t.editable) return
    insertRow(t)
    await tick()
    const row = (source?.length ?? 1) - 1
    if (showDocs) return openEditor(row, docCol)
    grid?.setSelection(row, 0)
    grid?.focus()
  }

  function duplicate() {
    const rows = selectedRows()
    if (!rows.length) return
    duplicateRows(t, rows)
  }

  function onkey(e: KeyboardEvent, chord: string): boolean {
    if (!t.editable) return false
    switch (chord) {
      case 'Delete':
        deleteRows(t, selectedRows())
        return true
      case 'Insert':
      case 'Alt+Insert':
        void addRow()
        return true
      case 'Mod+Shift+N':
        toggleNull()
        return true
      case 'Mod+D':
        duplicate()
        return true
    }
    return false
  }

  function menuItems(): MenuEntry[] {
    const sel = grid?.selection()
    const out: MenuEntry[] = []
    if (t.editable && sel) {
      out.push(
        { label: 'Edit cell', icon: 'edit', hint: 'F2', run: () => grid?.beginEdit(sel.r0, sel.c0) },
        { label: 'Open in editor…', run: () => openEditor(sel.r0, sel.c0) },
        { label: 'Set NULL', hint: 'Ctrl+Shift+N', run: toggleNull },
        'separator',
        { label: 'Add row', icon: 'rowAdd', hint: 'Insert', run: () => void addRow() },
        { label: 'Duplicate row', hint: 'Ctrl+D', run: duplicate },
        { label: 'Delete rows', icon: 'trash', hint: 'Delete', danger: true, run: () => deleteRows(t, selectedRows()) },
        { label: 'Revert row changes', icon: 'undo', run: () => revertRows(t, selectedRows()) },
      )
    }
    if (out.length) out.push('separator')
    out.push({ label: 'Export data…', icon: 'export', run: onexport })
    return out
  }

  function copyOptions(): CopyOptions {
    const schema = schemaOfObject(t.node.path, connections.config(tab.connId)?.kind)
    return { table: schema ? `${quoteIdent(schema)}.${quoteIdent(t.node.name)}` : quoteIdent(t.node.name), dialect: connections.config(tab.connId)?.kind ?? '' }
  }

  function onsort(col: number, add: boolean) {
    const name = source?.columns[col]?.name
    if (name) void setOrder(tab, cycleOrder(t.order, name, add))
  }

  function filterKey(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault()
      void applyFilter(tab, t.draft)
    } else if (e.key === 'Escape' && t.draft !== t.filter) {
      e.stopPropagation()
      t.draft = t.filter
    }
  }

  function count(n: number): string {
    return n.toLocaleString('en-US')
  }

  const apply = () => guard('Apply changes', () => applyChanges(tab, grid?.scrollPos() ?? null))
</script>

<div class="data">
  <div class="toolbar">
    <div class="filter" class:active={!!t.filter} title={filterUI.title}>
      <span class="kw">{filterUI.kw}</span>
      <input
        class="filter-input"
        type="text"
        spellcheck="false"
        placeholder={filterUI.placeholder}
        list="filters-{tab.key}"
        bind:value={t.draft}
        onkeydown={filterKey}
      />
      <datalist id="filters-{tab.key}">
        {#each history as f (f)}<option value={f}></option>{/each}
      </datalist>
      {#if t.filter || t.draft}
        <button class="icon-btn" type="button" title="Clear filter" onclick={() => applyFilter(tab, '')}><Icon name="close" size={11} /></button>
      {/if}
    </div>
    {#if t.order.length}
      <span class="order" title="Server-side order; click headers to change">ORDER BY {orderBy(t.order).join(', ')}</span>
    {/if}
    <div class="quick" title="Filter loaded rows">
      <Icon name="search" size={12} />
      <input class="quick-input" type="text" spellcheck="false" placeholder="Quick filter" bind:value={t.quick} />
    </div>
    <button class="icon-btn" type="button" title="Refresh (Ctrl+Enter)" disabled={!!tab.busy} onclick={() => guard('Refresh', () => loadData(tab))}>
      <Icon name="refresh" />
    </button>
    {#if docCol >= 0}
      <div class="seg" role="radiogroup" aria-label="Show as">
        <button type="button" role="radio" aria-checked={t.documents} class:on={t.documents} onclick={() => (t.documents = true)}>Documents</button>
        <button type="button" role="radio" aria-checked={!t.documents} class:on={!t.documents} onclick={() => (t.documents = false)}>Grid</button>
      </div>
    {/if}
    {#if t.editable}
      <button class="icon-btn" type="button" title={showDocs ? 'Add document' : 'Add row (Insert)'} onclick={addRow}><Icon name="rowAdd" /></button>
    {/if}
    {#if canImport(tab.connId, t.node.kind)}
      <button class="icon-btn" type="button" title="Import CSV…" onclick={() => importCsv(tab.connId, t.node)}><Icon name="open" /></button>
    {/if}
    <button class="icon-btn" type="button" title="Export data…" onclick={onexport}><Icon name="export" /></button>
  </div>

  {#if snap.errors.length}
    <div class="banner error">
      {#each snap.errors as m, i (i)}<div>{m.text}{#if m.detail} · {m.detail}{/if}{#if m.hint} · Hint: {m.hint}{/if}</div>{/each}
    </div>
  {/if}

  <div class="grid-wrap">
    {#if source && showDocs}
      <DocumentsView
        {source}
        col={docCol}
        version={tab.version + t.epoch}
        editable={t.editable}
        onedit={(row) => openEditor(row, docCol)}
        ondelete={(row) => deleteRows(t, [row])}
      />
    {:else if source}
      <Grid
        bind:this={grid}
        buffer={source}
        version={tab.version}
        epoch={t.epoch}
        oncell={openCell}
        {copyOptions}
        sortOf={(c) => sortOf(t.order, source.columns[c]?.name ?? '')}
        {onsort}
        rowClass={(i) => source.state(i)}
        cellClass={(i, c) => (source.edited(i, c) ? 'edited' : source.state(i) === 'new' && source.value(i, c) === undefined ? 'default' : '')}
        editable={t.editable}
        onbeginedit={beginEdit}
        oncommitedit={(row, col, text) => setCell(t, row, col, text)}
        {onkey}
        {menuItems}
        ownKeys={OWN_KEYS}
      />
    {:else if tab.busy}
      <p class="empty"><span class="spinner"></span> Loading {t.node.name}…</p>
    {:else if !snap.errors.length}
      <p class="empty">No data loaded.</p>
    {/if}
  </div>

  <footer>
    {#if source}
      <span>
        {#if source.index}<b>{count(source.index.length)}</b> of {count(snap.rows)} rows{:else}<b>{count(snap.rows)}</b> {snap.rows === 1 ? 'row' : 'rows'}{/if}
      </span>
      {#if snap.truncated}<span class="warn">row limit reached</span>{/if}
      {#if tab.busy}
        <span class="muted">loading…</span>
      {:else if snap.elapsedMs}
        <span class="muted">{count(snap.elapsedMs)} ms</span>
      {/if}
      {#if t.total !== null}
        <span class="muted" title="Rows the filter matches on the server">of {count(t.total)}</span>
      {:else if snap.hasMore && flavor !== 'redis'}
        <button class="btn small" type="button" disabled={t.counting} title="Count the rows the filter matches (may be slow on big tables)" onclick={() => guard('Count rows', () => countRows(tab))}>
          {#if t.counting}<span class="spinner"></span>{/if}Count all
        </button>
      {/if}
      {#if snap.hasMore}
        <span class="more">more rows available</span>
        <button class="btn small" type="button" disabled={!!tab.busy || !snap.fetchable} onclick={() => guard('Load more', () => tabs.fetch(tab, false))}>Load more</button>
        <button class="btn small" type="button" disabled={!!tab.busy || !snap.fetchable} onclick={() => guard('Load all', () => tabs.fetch(tab, true))}>Load all</button>
      {/if}
    {/if}
    <span class="spacer"></span>
    {#if t.key !== null && !t.editable}
      <span class="ro" title={t.keyError}><Icon name="lock" size={11} /> read-only{t.keyError ? `: ${t.keyError}` : ''}</span>
    {/if}
    {#if t.applyError}<span class="apply-error" title={t.applyError}>{t.applyError}</span>{/if}
    {#if t.changes}
      <span class="pending">{t.changes} pending {t.changes === 1 ? 'change' : 'changes'}</span>
      <button class="btn small" type="button" disabled={t.applying} onclick={() => discardChanges(t)}>Discard</button>
      <button class="btn small primary" type="button" disabled={t.applying} title="Apply in one transaction (Ctrl+S)" onclick={apply}>
        {#if t.applying}<span class="spinner"></span>{/if} Apply
      </button>
    {/if}
  </footer>
</div>

<style>
  .data {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }

  .toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    padding: 0 8px;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
  }

  .filter {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    height: 24px;
    padding: 0 4px 0 8px;
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 5px;
  }

  .filter:focus-within {
    border-color: var(--accent);
  }

  .filter.active {
    border-color: var(--yellow);
  }

  .kw {
    color: var(--mauve);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 700;
  }

  .filter-input,
  .quick-input {
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    outline: none;
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .order {
    max-width: 30%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--overlay1);
    font-family: var(--font-mono);
    font-size: 11px;
  }

  .seg {
    display: flex;
    border: 1px solid var(--surface1);
    border-radius: 5px;
    overflow: hidden;
  }

  .seg button {
    padding: 2px 8px;
    border: 0;
    background: var(--crust);
    color: var(--subtext0);
    cursor: pointer;
    font-size: 12px;
  }

  .seg button.on {
    background: color-mix(in srgb, var(--accent) 18%, var(--crust));
    color: var(--text);
  }

  .quick {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 180px;
    height: 24px;
    padding: 0 8px;
    color: var(--overlay0);
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 5px;
  }

  .quick:focus-within {
    border-color: var(--accent);
  }

  .banner {
    padding: 6px 12px;
    font-family: var(--font-mono);
    font-size: 12px;
    white-space: pre-wrap;
  }

  .banner.error {
    color: var(--red);
    background: color-mix(in srgb, var(--red) 8%, transparent);
    border-bottom: 1px solid color-mix(in srgb, var(--red) 40%, transparent);
  }

  .grid-wrap {
    flex: 1;
    min-height: 0;
  }

  .empty {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 12px;
    color: var(--overlay0);
  }

  footer {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 32px;
    padding: 0 10px;
    background: var(--mantle);
    border-top: 1px solid var(--surface0);
    color: var(--subtext0);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  footer b {
    color: var(--text);
  }

  .spacer {
    flex: 1;
  }

  .muted {
    color: var(--overlay0);
  }

  .warn {
    color: var(--peach);
  }

  .more {
    color: var(--yellow);
  }

  .ro {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--overlay1);
  }

  .pending {
    color: var(--peach);
    font-weight: 600;
  }

  .apply-error {
    max-width: 40%;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--red);
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }
</style>
