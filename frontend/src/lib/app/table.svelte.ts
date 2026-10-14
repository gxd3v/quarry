// Table tabs: a table or view browsed with a filter and server-side order,
// edited inline with pending changes applied in one transaction, and
// described (columns, indexes, constraints, triggers, DDL, stats).
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { dataClient, explorerClient } from '../api/clients'
import { errorMessage, needsConfirmation } from '../api/errors'
import type { Row, ValueSchema } from '../gen/quarry/v1/common_pb'
import type { DescribeResponse } from '../gen/quarry/v1/explorer_pb'
import type { ResultBuffer } from '../grid/buffer'
import type { ResultSet } from '../run/model'
import { scopeOfPath } from '../sql'
import { counts, describeCounts, NO_CHANGES, pendingCount, reduce, rowChanges, type CellValue, type Pending, type PendingAction } from '../table/changes'
import { orderBy, type OrderTerm } from '../table/order'
import { TableSource } from '../table/source'
import { connections } from './connections.svelte'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import type { TableNodeRef, TableSnapshot } from './session'
import { tabs, type Tab } from './tabs.svelte'

export type TableView = 'data' | 'columns' | 'indexes' | 'constraints' | 'triggers' | 'ddl' | 'stats'

export const TABLE_VIEWS: { view: TableView; label: string }[] = [
  { view: 'data', label: 'Data' },
  { view: 'columns', label: 'Columns' },
  { view: 'indexes', label: 'Indexes' },
  { view: 'constraints', label: 'Constraints' },
  { view: 'triggers', label: 'Triggers' },
  { view: 'ddl', label: 'DDL' },
  { view: 'stats', label: 'Stats' },
]

const FILTER_HISTORY_KEY = 'quarry.filters'
const FILTER_HISTORY_MAX = 20

export class TableState {
  readonly node: TableNodeRef
  // The applied filter (SQL WHERE body) and the one being typed.
  filter = $state('')
  draft = $state('')
  order = $state.raw<OrderTerm[]>([])
  // Client-side filter on loaded rows.
  quick = $state('')
  view = $state<TableView>('data')
  // Key columns; null until known, empty when rows cannot be edited.
  key = $state.raw<string[] | null>(null)
  keyError = $state('')
  pending = $state.raw<Pending>(NO_CHANGES)
  describe = $state.raw<DescribeResponse | null>(null)
  describeError = $state('')
  describing = $state(false)
  applying = $state(false)
  applyError = $state('')
  // Bumped when the grid's view of the rows changes without new rows.
  epoch = $state(0)
  // Scroll position to restore after the next load.
  restore: { top: number; left: number } | null = null
  loaded = false
  // Exact timestamps of key cells (row * columns + column), which the grid
  // only keeps as local display text.
  times = new Map<number, Timestamp>()
  #source: TableSource | null = null

  constructor(node: TableNodeRef, snap?: Partial<TableSnapshot>) {
    this.node = { path: [...node.path], kind: node.kind, name: node.name }
    this.filter = this.draft = snap?.filter ?? ''
    this.order = snap?.order ?? []
    if (snap?.view && TABLE_VIEWS.some((v) => v.view === snap.view)) this.view = snap.view as TableView
  }

  get editable(): boolean {
    return !!this.key?.length
  }

  get changes(): number {
    return pendingCount(this.pending)
  }

  // source returns the grid's view of buffer, made once per loaded result.
  source(buffer: ResultBuffer): TableSource {
    if (this.#source?.buffer !== buffer) {
      this.#source = new TableSource(buffer)
      this.#source.pending = this.pending
    }
    return this.#source
  }

  get current(): TableSource | null {
    return this.#source
  }

  apply(a: PendingAction) {
    this.pending = reduce(this.pending, a)
    if (this.#source) this.#source.pending = this.pending
    this.applyError = ''
    this.epoch++
  }

  snapshot(): TableSnapshot {
    return { node: this.node, filter: this.filter, order: this.order, view: this.view }
  }
}

function sameNode(a: TableNodeRef, b: TableNodeRef): boolean {
  return a.path.length === b.path.length && a.path.every((p, i) => p === b.path[i])
}

// openTable shows the table tab of a node, opening one when needed.
export function openTable(connId: string, node: TableNodeRef, snap?: Partial<TableSnapshot>, title = ''): Tab {
  const open = tabs.tabs.find((t) => t.connId === connId && t.table && sameNode(t.table.node, node))
  if (open) {
    tabs.activate(open)
    return open
  }
  const state = new TableState(node, snap)
  return tabs.open(connId, scopeOfPath(node.path), '', { title: title || node.name, table: state })
}

// tableNode builds the explorer node of a catalog object (PostgreSQL layout:
// [database, schema, folder, name]).
export function tableNode(database: string, schema: string, name: string, kind: string, detail = ''): TableNodeRef {
  const folder = kind === 'view' ? (/materialized/i.test(detail) ? 'matviews' : 'views') : 'tables'
  return { path: [database, schema, folder, name], kind, name }
}

function wireNode(n: TableNodeRef) {
  return { path: n.path, kind: n.kind, name: n.name }
}

// confirmDiscard asks before an action drops pending changes; true to go on.
async function confirmDiscard(tab: Tab, what: string): Promise<boolean> {
  const t = tab.table!
  if (!t.changes) return true
  const n = t.changes
  const ok = await prompts.ask({
    title: what,
    text: `${tab.title} has ${n} pending ${n === 1 ? 'change' : 'changes'} that were not applied. Discard them?`,
    confirm: 'Discard changes',
    danger: true,
  })
  if (ok) t.apply({ type: 'discard' })
  return ok
}

// loadData (re)loads the rows with the current filter and order. keep loads
// at least as many rows as are shown and restores the scroll position.
export async function loadData(tab: Tab, opts: { keep?: { top: number; left: number } | null; force?: boolean } = {}) {
  const t = tab.table
  if (!t) return
  if (!opts.force && !(await confirmDiscard(tab, 'Reload data'))) return
  t.loaded = true
  t.times = new Map()
  t.restore = opts.keep ?? null
  const shown = tab.run.results[0]?.buffer.length ?? 0
  const maxRows = opts.keep && shown > 10_000 ? shown : 0
  // Key timestamps are kept as rows arrive, so the key must be known first.
  if (t.key === null) await loadKey(tab)
  await tabs.openData(tab, { filter: t.filter, orderBy: orderBy(t.order), maxRows }, (r, rows, start, kept) => keepTimes(t, r, rows, start, kept))
}

// keepTimes remembers the exact timestamps of key cells.
function keepTimes(t: TableState, r: ResultSet, rows: readonly Row[], start: number, kept: number) {
  const key = t.key
  if (!key?.length) return
  const n = r.columns.length
  const cols = key.map((k) => r.columns.findIndex((c) => c.name === k)).filter((c) => c >= 0)
  for (let i = 0; i < kept; i++) {
    for (const c of cols) {
      const v = rows[i].values[c]?.kind
      if (v?.case === 'time') t.times.set((start + i) * n + c, v.value)
    }
  }
}

export async function loadKey(tab: Tab) {
  const t = tab.table
  if (!t) return
  try {
    if (connections.config(tab.connId)?.readOnly) {
      t.key = []
      t.keyError = 'read-only connection'
      return
    }
    const res = await connections.withConnection(tab.connId, () => dataClient().rowKey({ connId: tab.connId, node: wireNode(t.node) }))
    t.key = res.columns
    t.keyError = res.columns.length ? '' : t.node.kind === 'table' ? 'no primary key or unique index' : `a ${t.node.kind} cannot be edited`
  } catch (err) {
    t.key = []
    t.keyError = errorMessage(err)
  }
}

export async function loadDescribe(tab: Tab, refresh = false) {
  const t = tab.table
  if (!t || t.describing || (t.describe && !refresh)) return
  t.describing = true
  t.describeError = ''
  try {
    t.describe = await connections.withConnection(tab.connId, () => explorerClient().describe({ connId: tab.connId, node: wireNode(t.node) }))
  } catch (err) {
    t.describeError = errorMessage(err)
  } finally {
    t.describing = false
  }
}

// applyFilter applies the filter bar's text and reloads.
export async function applyFilter(tab: Tab, filter: string) {
  const t = tab.table
  if (!t) return
  const prev = t.filter
  t.filter = filter.trim()
  t.draft = t.filter
  if (!(await confirmDiscard(tab, 'Apply filter'))) {
    t.filter = prev
    return
  }
  if (t.filter) rememberFilter(tab.connId, t.node, t.filter)
  await loadData(tab, { force: true })
}

export async function setOrder(tab: Tab, order: OrderTerm[]) {
  const t = tab.table
  if (!t || !(await confirmDiscard(tab, 'Sort'))) return
  t.order = order
  await loadData(tab, { force: true })
}

// --- filter history (per table, in local storage) ---

function historyKey(connId: string, node: TableNodeRef): string {
  return `${connId}:${node.path.join('/')}`
}

function readHistory(): Record<string, string[]> {
  try {
    const v = JSON.parse(localStorage.getItem(FILTER_HISTORY_KEY) ?? '{}') as unknown
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, string[]>) : {}
  } catch {
    return {}
  }
}

export function filterHistory(connId: string, node: TableNodeRef): string[] {
  const list = readHistory()[historyKey(connId, node)]
  return Array.isArray(list) ? list.filter((x) => typeof x === 'string') : []
}

function rememberFilter(connId: string, node: TableNodeRef, filter: string) {
  try {
    const all = readHistory()
    const k = historyKey(connId, node)
    all[k] = [filter, ...(all[k] ?? []).filter((f) => f !== filter)].slice(0, FILTER_HISTORY_MAX)
    localStorage.setItem(FILTER_HISTORY_KEY, JSON.stringify(all))
  } catch {
    // Storage may be unavailable; history is a convenience.
  }
}

// --- editing ---

// keyValue is the exact value of a loaded key cell: timestamps as they came,
// everything else as its untruncated text, which the server converts to the
// column's type.
export function keyValue(t: TableState, buffer: ResultBuffer, row: number, col: number): MessageInitShape<typeof ValueSchema> {
  const ts = t.times.get(row * buffer.columns.length + col)
  if (ts) return { kind: { case: 'time', value: ts } }
  const v = buffer.text(row, col)
  return v === null ? { kind: { case: 'null', value: true } } : { kind: { case: 'text', value: v } }
}

// setCell records a new value for a cell of the shown rows.
export function setCell(t: TableState, viewRow: number, col: number, value: CellValue) {
  const src = t.current
  const ref = src?.ref(viewRow)
  if (!src || !ref || src.state(viewRow) === 'deleted') return
  t.apply({ type: 'set', row: ref, col, value, original: src.original(viewRow, col) ?? undefined })
}

// insertRow adds a new row, copying values when duplicating.
export function insertRow(t: TableState, values?: Map<number, CellValue>) {
  t.apply({ type: 'insert', values })
}

export function duplicateRows(t: TableState, rows: number[]) {
  const src = t.current
  if (!src) return
  const key = new Set((t.key ?? []).map((k) => src.columns.findIndex((c) => c.name === k)))
  for (const r of rows) {
    const values = new Map<number, CellValue>()
    src.columns.forEach((_, c) => {
      const v = src.value(r, c)
      // Keys are left out so they get their defaults (or must be typed).
      if (v !== undefined && !key.has(c)) values.set(c, v)
    })
    insertRow(t, values)
  }
}

export function deleteRows(t: TableState, rows: number[]) {
  const src = t.current
  if (!src) return
  const refs = rows.map((r) => src.ref(r)).filter((r) => r !== null)
  if (refs.length) t.apply({ type: 'delete', rows: refs })
}

export function revertRows(t: TableState, rows: number[]) {
  const src = t.current
  if (!src) return
  const refs = rows.map((r) => src.ref(r)).filter((r) => r !== null)
  if (refs.length) t.apply({ type: 'revert', rows: refs })
}

export function discardChanges(t: TableState) {
  t.apply({ type: 'discard' })
}

// applyChanges saves the pending changes in one transaction, asking first on
// prod, then reloads keeping the scroll position. On failure the changes stay.
export async function applyChanges(tab: Tab, keep: { top: number; left: number } | null = null): Promise<boolean> {
  const t = tab.table
  const buffer = tab.run.results[0]?.buffer
  if (!t || !buffer || !t.changes || t.applying) return false
  // Commit an inline edit in progress.
  if (document.activeElement instanceof HTMLElement && document.activeElement.closest('.cell-editor')) document.activeElement.blur()
  const key = t.key ?? []
  const columns = buffer.columns.map((c) => c.name)
  const keyCols = key.map((k) => columns.indexOf(k))
  if (!key.length || keyCols.some((c) => c < 0)) {
    t.applyError = 'This table has no row key in the loaded columns; changes cannot be saved.'
    return false
  }
  const changes = rowChanges(t.pending, { columns, keyCols, key: (row, col) => keyValue(t, buffer, row, col) })
  const c = counts(t.pending)
  const rows = (n: number) => `${n.toLocaleString('en-US')} ${n === 1 ? 'row' : 'rows'}`
  const items = [
    ...(c.updates ? [{ title: 'UPDATE', detail: `${rows(c.updates)}, ${c.cells.toLocaleString('en-US')} ${c.cells === 1 ? 'cell' : 'cells'}`, code: '' }] : []),
    ...(c.inserts ? [{ title: 'INSERT', detail: rows(c.inserts), code: '' }] : []),
    ...(c.deletes ? [{ title: 'DELETE', detail: rows(c.deletes), code: '' }] : []),
  ]
  const env = connections.env(tab.connId)
  const name = connections.name(tab.connId)
  const review = () =>
    prompts.reviewList({
      title: env === 'prod' ? `Apply on PROD: ${name}` : 'Apply changes',
      text: `Save ${describeCounts(c).join(', ')} to ${t.node.name} in one transaction?`,
      items,
      confirm: 'Apply',
      env,
    })
  let confirmed = false
  if (env === 'prod') {
    if (!(await review())) return false
    confirmed = true
  }
  t.applying = true
  t.applyError = ''
  try {
    for (;;) {
      try {
        await connections.withConnection(tab.connId, () => dataClient().applyChanges({ connId: tab.connId, node: wireNode(t.node), changes, confirmed }))
        break
      } catch (err) {
        if (confirmed || !needsConfirmation(err)) throw err
        if (!(await review())) return false
        confirmed = true
      }
    }
  } catch (err) {
    t.applyError = errorMessage(err)
    return false
  } finally {
    t.applying = false
  }
  const n = t.changes
  t.apply({ type: 'discard' })
  notify.flash(`Applied ${n} ${n === 1 ? 'change' : 'changes'} to ${t.node.name}`)
  await loadData(tab, { keep, force: true })
  return true
}
