// Pending edits of a table view, kept apart from the loaded rows until they
// are applied in one transaction. Pure: the store keeps the current state and
// replaces it through reduce.
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Value, ValueSchema } from '../gen/quarry/v1/common_pb'
import { ChangeOp, type RowChangeSchema } from '../gen/quarry/v1/data_pb'

// A cell value as edited: text the server converts to the column's type, or
// null for NULL.
export type CellValue = string | null

// A row of the view: a loaded row by its index in the result, or a new row.
export type RowRef = { kind: 'old'; index: number } | { kind: 'new'; id: number }

export interface NewRow {
  id: number
  // Column index → value; columns left out get their defaults.
  values: ReadonlyMap<number, CellValue>
}

export interface Pending {
  // Loaded row → column → new value.
  edits: ReadonlyMap<number, ReadonlyMap<number, CellValue>>
  deleted: ReadonlySet<number>
  inserted: readonly NewRow[]
  seq: number
}

export const NO_CHANGES: Pending = { edits: new Map(), deleted: new Set(), inserted: [], seq: 0 }

export type PendingAction =
  // original is the loaded value; setting it back drops the edit.
  | { type: 'set'; row: RowRef; col: number; value: CellValue; original?: CellValue }
  | { type: 'insert'; values?: ReadonlyMap<number, CellValue> }
  | { type: 'delete'; rows: readonly RowRef[] }
  | { type: 'revert'; rows: readonly RowRef[] }
  | { type: 'discard' }

export function reduce(p: Pending, a: PendingAction): Pending {
  switch (a.type) {
    case 'set': {
      if (a.row.kind === 'new') {
        const id = a.row.id
        return { ...p, inserted: p.inserted.map((r) => (r.id === id ? { id, values: new Map(r.values).set(a.col, a.value) } : r)) }
      }
      const i = a.row.index
      if (p.deleted.has(i)) return p
      const row = new Map(p.edits.get(i))
      if (a.original !== undefined && a.original === a.value) row.delete(a.col)
      else row.set(a.col, a.value)
      const edits = new Map(p.edits)
      if (row.size) edits.set(i, row)
      else edits.delete(i)
      return { ...p, edits }
    }
    case 'insert': {
      const seq = p.seq + 1
      return { ...p, seq, inserted: [...p.inserted, { id: seq, values: new Map(a.values) }] }
    }
    case 'delete': {
      const deleted = new Set(p.deleted)
      const gone = new Set<number>()
      for (const r of a.rows) {
        if (r.kind === 'old') deleted.add(r.index)
        else gone.add(r.id)
      }
      return { ...p, deleted, inserted: gone.size ? p.inserted.filter((r) => !gone.has(r.id)) : p.inserted }
    }
    case 'revert': {
      const edits = new Map(p.edits)
      const deleted = new Set(p.deleted)
      const gone = new Set<number>()
      for (const r of a.rows) {
        if (r.kind === 'old') {
          edits.delete(r.index)
          deleted.delete(r.index)
        } else {
          gone.add(r.id)
        }
      }
      return { ...p, edits, deleted, inserted: p.inserted.filter((r) => !gone.has(r.id)) }
    }
    case 'discard':
      return { ...NO_CHANGES, seq: p.seq }
  }
}

export interface ChangeCounts {
  updates: number
  // Edited cells of the updated rows.
  cells: number
  inserts: number
  deletes: number
}

export function counts(p: Pending): ChangeCounts {
  let updates = 0
  let cells = 0
  for (const [i, row] of p.edits) {
    if (p.deleted.has(i)) continue
    updates++
    cells += row.size
  }
  return { updates, cells, inserts: p.inserted.length, deletes: p.deleted.size }
}

// pendingCount is what the "N pending changes" counter shows: edited cells,
// new rows and deleted rows.
export function pendingCount(p: Pending): number {
  const c = counts(p)
  return c.cells + c.inserts + c.deletes
}

export function describeCounts(c: ChangeCounts): string[] {
  const n = (k: number, one: string, many = one + 's') => `${k.toLocaleString('en-US')} ${k === 1 ? one : many}`
  const out: string[] = []
  if (c.updates) out.push(`${n(c.updates, 'row')} updated (${n(c.cells, 'cell')})`)
  if (c.inserts) out.push(`${n(c.inserts, 'row')} inserted`)
  if (c.deletes) out.push(`${n(c.deletes, 'row')} deleted`)
  return out
}

export function cellValue(v: CellValue): MessageInitShape<typeof ValueSchema> {
  return v === null ? { kind: { case: 'null', value: true } } : { kind: { case: 'text', value: v } }
}

export interface ChangeContext {
  // Column names of the result, by index.
  columns: readonly string[]
  // Indexes of the key columns.
  keyCols: readonly number[]
  // The exact value of a loaded cell, for keys.
  key: (row: number, col: number) => MessageInitShape<typeof ValueSchema> | Value
}

// rowChanges turns pending edits into ApplyChanges input: deletes first (so a
// new row can take a deleted row's key), then updates, then inserts. Keys come
// from the loaded rows, never from edited values.
export function rowChanges(p: Pending, ctx: ChangeContext): MessageInitShape<typeof RowChangeSchema>[] {
  const keyOf = (row: number) => Object.fromEntries(ctx.keyCols.map((c) => [ctx.columns[c], ctx.key(row, c)]))
  const valuesOf = (m: ReadonlyMap<number, CellValue>) =>
    Object.fromEntries([...m].sort((a, b) => a[0] - b[0]).map(([c, v]) => [ctx.columns[c], cellValue(v)]))
  const out: MessageInitShape<typeof RowChangeSchema>[] = []
  for (const i of [...p.deleted].sort((a, b) => a - b)) out.push({ op: ChangeOp.DELETE, key: keyOf(i) })
  for (const i of [...p.edits.keys()].sort((a, b) => a - b)) {
    if (p.deleted.has(i)) continue
    out.push({ op: ChangeOp.UPDATE, key: keyOf(i), values: valuesOf(p.edits.get(i)!) })
  }
  for (const r of p.inserted) out.push({ op: ChangeOp.INSERT, values: valuesOf(r.values) })
  return out
}
