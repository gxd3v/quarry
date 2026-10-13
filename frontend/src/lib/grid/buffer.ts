import type { Column, Row } from '../gen/quarry/v1/common_pb'
import { bytesHex, formatValue, isNumericType } from '../values'

export type Cell = string | null

// Value kind of a column (the protobuf Value case of its first non-NULL
// cell): text, int, float, number, bool, bytes, time, json, array.
export type ValueKind = string

export interface GridColumn {
  name: string
  type: string
  numeric: boolean
  // Unknown until a non-NULL value arrives.
  kind?: ValueKind
}

// Cells of a row are joined into one flat string, so a million rows cost a
// million strings instead of millions of small strings and arrays. Display
// text never contains C0 controls (see formatValue), which makes these safe.
const SEP = '\u001f'
const NULL = '\u0000'

// ResultBuffer stores display text for streamed rows. It is plain (not
// reactive): owners bump a version counter after appending. Cells whose
// display text is lossy (clipped, multi-line, long bytes) keep their full
// value on the side, so viewers and copies get the real content.
export class ResultBuffer {
  readonly columns: GridColumn[]
  readonly limit: number
  truncated = false
  #rows: string[] = []
  #scratch: string[]
  // row * columns + column → full value.
  #full = new Map<number, string | Uint8Array>()
  #untyped: number

  constructor(columns: readonly Pick<Column, 'name' | 'type'>[], limit = Infinity) {
    this.columns = columns.map((c) => ({ name: c.name, type: c.type, numeric: isNumericType(c.type) }))
    this.limit = limit
    this.#scratch = new Array<string>(this.columns.length)
    this.#untyped = this.columns.length
  }

  get length(): number {
    return this.#rows.length
  }

  // Number of cells holding a full value beside their display text.
  get fullCount(): number {
    return this.#full.size
  }

  // appendRows formats and stores proto rows, returning how many were kept.
  appendRows(rows: readonly Row[]): number {
    const n = this.columns.length
    const s = this.#scratch
    const full = this.#full
    let key = 0
    const keep = (v: string | Uint8Array) => full.set(key, v)
    let kept = 0
    for (const row of rows) {
      if (!this.#room()) break
      const values = row.values
      const base = this.#rows.length * n
      if (this.#untyped) this.#learnKinds(row)
      for (let c = 0; c < n; c++) {
        key = base + c
        s[c] = formatValue(values[c], keep) ?? NULL
      }
      this.#rows.push(s.join(SEP))
      kept++
    }
    return kept
  }

  // appendCells stores already formatted cells; they must not contain C0 controls.
  appendCells(cells: readonly Cell[]): boolean {
    if (!this.#room()) return false
    const s = this.#scratch
    for (let c = 0; c < s.length; c++) s[c] = cells[c] ?? NULL
    this.#rows.push(s.join(SEP))
    return true
  }

  row(i: number): Cell[] {
    const raw = this.#rows[i]
    if (raw === undefined || this.columns.length === 0) return []
    const cells: Cell[] = raw.split(SEP)
    for (let c = 0; c < cells.length; c++) if (cells[c] === NULL) cells[c] = null
    return cells
  }

  // full returns the complete value of a cell whose display text is lossy.
  full(row: number, col: number): string | Uint8Array | undefined {
    if (col < 0 || col >= this.columns.length) return undefined
    return this.#full.get(row * this.columns.length + col)
  }

  // text returns the untruncated text of a cell (bytes as \x hex), or null.
  text(row: number, col: number, cells = this.row(row)): Cell {
    const f = this.full(row, col)
    if (f === undefined) return cells[col] ?? null
    return typeof f === 'string' ? f : bytesHex(f)
  }

  // rows returns untruncated cell text for rows [r0, r1] and columns [c0, c1].
  slice(r0: number, r1: number, c0: number, c1: number): Cell[][] {
    const out: Cell[][] = []
    for (let r = Math.max(0, r0); r <= Math.min(r1, this.length - 1); r++) {
      const cells = this.row(r)
      const line: Cell[] = []
      for (let c = c0; c <= c1; c++) line.push(this.text(r, c, cells))
      out.push(line)
    }
    return out
  }

  #learnKinds(row: Row) {
    for (let c = 0; c < this.columns.length; c++) {
      const col = this.columns[c]
      if (col.kind) continue
      const k = row.values[c]?.kind.case
      if (k && k !== 'null') {
        col.kind = k
        this.#untyped--
      }
    }
  }

  #room(): boolean {
    if (this.#rows.length < this.limit) return true
    this.truncated = true
    return false
  }
}
