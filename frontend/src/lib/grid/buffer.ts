import type { Column, Row } from '../gen/quarry/v1/common_pb'
import { formatValue, isNumericType } from '../values'

export type Cell = string | null

export interface GridColumn {
  name: string
  type: string
  numeric: boolean
}

// Cells of a row are joined into one flat string, so a million rows cost a
// million strings instead of millions of small strings and arrays. Display
// text never contains C0 controls (see formatValue), which makes these safe.
const SEP = '\u001f'
const NULL = '\u0000'

// ResultBuffer stores display text for streamed rows. It is plain (not
// reactive): owners bump a version counter after appending.
export class ResultBuffer {
  readonly columns: GridColumn[]
  readonly limit: number
  truncated = false
  #rows: string[] = []
  #scratch: string[]

  constructor(columns: readonly Pick<Column, 'name' | 'type'>[], limit = Infinity) {
    this.columns = columns.map((c) => ({ name: c.name, type: c.type, numeric: isNumericType(c.type) }))
    this.limit = limit
    this.#scratch = new Array<string>(this.columns.length)
  }

  get length(): number {
    return this.#rows.length
  }

  // appendRows formats and stores proto rows, returning how many were kept.
  appendRows(rows: readonly Row[]): number {
    const n = this.columns.length
    const s = this.#scratch
    let kept = 0
    for (const row of rows) {
      if (!this.#room()) break
      const values = row.values
      for (let c = 0; c < n; c++) s[c] = formatValue(values[c]) ?? NULL
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

  #room(): boolean {
    if (this.#rows.length < this.limit) return true
    this.truncated = true
    return false
  }
}
