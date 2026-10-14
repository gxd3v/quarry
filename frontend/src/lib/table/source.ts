// TableSource is the grid's view of a table tab: the loaded rows (optionally
// narrowed by the quick filter) with pending edits laid over them, followed
// by the new rows. It is plain; the owner bumps an epoch after changing it.
import type { Cell, GridColumn, GridSource, ResultBuffer } from '../grid/buffer'
import { displayText } from '../values'
import { NO_CHANGES, type CellValue, type Pending, type RowRef } from './changes'

export type RowState = '' | 'new' | 'deleted'

export class TableSource implements GridSource {
  readonly buffer: ResultBuffer
  pending: Pending = NO_CHANGES
  // Loaded rows the quick filter keeps, or null for all of them.
  index: Int32Array | null = null

  constructor(buffer: ResultBuffer) {
    this.buffer = buffer
  }

  get columns(): GridColumn[] {
    return this.buffer.columns
  }

  // Loaded rows shown.
  get loaded(): number {
    return this.index ? this.index.length : this.buffer.length
  }

  get length(): number {
    return this.loaded + this.pending.inserted.length
  }

  ref(i: number): RowRef | null {
    const n = this.loaded
    if (i < 0 || i >= this.length) return null
    if (i < n) return { kind: 'old', index: this.index ? this.index[i] : i }
    return { kind: 'new', id: this.pending.inserted[i - n].id }
  }

  // viewIndex finds the view row of a reference, or -1 when it is filtered out.
  viewIndex(r: RowRef): number {
    if (r.kind === 'new') {
      const j = this.pending.inserted.findIndex((x) => x.id === r.id)
      return j < 0 ? -1 : this.loaded + j
    }
    if (!this.index) return r.index < this.buffer.length ? r.index : -1
    let lo = 0
    let hi = this.index.length
    while (lo < hi) {
      const mid = (lo + hi) >>> 1
      if (this.index[mid] < r.index) lo = mid + 1
      else hi = mid
    }
    return this.index[lo] === r.index ? lo : -1
  }

  state(i: number): RowState {
    const r = this.ref(i)
    if (!r) return ''
    if (r.kind === 'new') return 'new'
    return this.pending.deleted.has(r.index) ? 'deleted' : ''
  }

  edited(i: number, col: number): boolean {
    const r = this.ref(i)
    if (!r) return false
    if (r.kind === 'new') return this.#newValues(r.id)?.has(col) ?? false
    return this.pending.edits.get(r.index)?.has(col) ?? false
  }

  // value returns the full current value of a cell, edited or loaded; for a
  // new row, undefined means the column keeps its default.
  value(i: number, col: number): CellValue | undefined {
    const r = this.ref(i)
    if (!r) return null
    if (r.kind === 'new') return this.#newValues(r.id)?.get(col)
    const e = this.pending.edits.get(r.index)
    if (e?.has(col)) return e.get(col)!
    return this.buffer.text(r.index, col)
  }

  // original returns the loaded value of a cell (undefined for new rows).
  original(i: number, col: number): CellValue | undefined {
    const r = this.ref(i)
    return r?.kind === 'old' ? this.buffer.text(r.index, col) : undefined
  }

  row(i: number): Cell[] {
    const r = this.ref(i)
    if (!r) return []
    if (r.kind === 'new') {
      const vals = this.#newValues(r.id)
      return this.columns.map((_, c) => {
        const v = vals?.get(c)
        return v === undefined ? DEFAULT : v === null ? null : displayText(v)
      })
    }
    const cells = this.buffer.row(r.index)
    const e = this.pending.edits.get(r.index)
    if (e) for (const [c, v] of e) cells[c] = v === null ? null : displayText(v)
    return cells
  }

  slice(r0: number, r1: number, c0: number, c1: number): Cell[][] {
    const out: Cell[][] = []
    for (let r = Math.max(0, r0); r <= Math.min(r1, this.length - 1); r++) {
      const line: Cell[] = []
      for (let c = c0; c <= c1; c++) line.push(this.value(r, c) ?? null)
      out.push(line)
    }
    return out
  }

  #newValues(id: number) {
    return this.pending.inserted.find((x) => x.id === id)?.values
  }
}

// Shown in new rows for columns left to their default.
export const DEFAULT = '(default)'
