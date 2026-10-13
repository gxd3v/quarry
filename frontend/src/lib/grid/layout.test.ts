import { describe, expect, it } from 'vitest'
import {
  columnOffsets,
  computeWindow,
  estimateWidths,
  gutterWidth,
  MAX_AUTO_COL_PX,
  MAX_SCROLL_PX,
  MIN_COL_PX,
  scrollRatio,
  spacerHeight,
  type WindowInput,
} from './layout'

const base: WindowInput = {
  scrollTop: 0,
  scrollLeft: 0,
  width: 500,
  height: 220,
  rowCount: 1000,
  rowHeight: 22,
  offsets: columnOffsets(Array(20).fill(100)),
  overscanRows: 2,
  overscanCols: 1,
}

describe('computeWindow', () => {
  it('covers the viewport plus overscan at the top', () => {
    expect(computeWindow(base)).toEqual({ rowStart: 0, rowEnd: 12, rowTop: 0, colStart: 0, colEnd: 6 })
  })

  it('follows scroll in both directions', () => {
    const w = computeWindow({ ...base, scrollTop: 22 * 100 + 5, scrollLeft: 250 })
    expect(w.rowStart).toBe(98)
    expect(w.rowEnd).toBe(113)
    expect(w.rowTop).toBe(98 * 22)
    expect(w.colStart).toBe(1)
    expect(w.colEnd).toBe(9)
  })

  it('clamps at the end of the data', () => {
    const w = computeWindow({ ...base, scrollTop: 1e9, scrollLeft: 1e9 })
    expect(w.rowEnd).toBe(1000)
    expect(w.rowStart).toBe(1000 - 10 - 2)
    expect(w.colEnd).toBe(20)
    expect(w.colStart).toBe(18)
  })

  it('handles empty results', () => {
    expect(computeWindow({ ...base, rowCount: 0, offsets: [0] })).toEqual({
      rowStart: 0,
      rowEnd: 0,
      rowTop: 0,
      colStart: 0,
      colEnd: 0,
    })
  })

  it('handles fewer rows than the viewport', () => {
    const w = computeWindow({ ...base, rowCount: 3, scrollTop: 50 })
    expect([w.rowStart, w.rowEnd, w.rowTop]).toEqual([0, 3, 0])
  })

  it('treats a column edge at the viewport start as not visible', () => {
    const w = computeWindow({ ...base, scrollLeft: 200, overscanCols: 0 })
    expect([w.colStart, w.colEnd]).toEqual([2, 7])
  })

  it('compresses very tall results and reaches the last row', () => {
    const rowCount = 5_000_000
    const input = { ...base, rowCount, overscanRows: 0 }
    expect(spacerHeight(rowCount, 22)).toBe(MAX_SCROLL_PX)
    const maxScroll = MAX_SCROLL_PX - base.height
    expect(scrollRatio(rowCount, 22, base.height)).toBeGreaterThan(1)

    const end = computeWindow({ ...input, scrollTop: maxScroll })
    expect(end.rowEnd).toBe(rowCount)
    // The last row sits flush with the viewport bottom.
    expect(end.rowTop + (rowCount - end.rowStart) * 22).toBe(MAX_SCROLL_PX)

    const mid = computeWindow({ ...input, scrollTop: maxScroll / 2 })
    expect(Math.abs(mid.rowStart - rowCount / 2)).toBeLessThan(10)
    // Rendered rows stay within the visible physical range.
    expect(mid.rowTop).toBeGreaterThanOrEqual(maxScroll / 2 - 22)
    expect(mid.rowTop).toBeLessThanOrEqual(maxScroll / 2)
  })

  it('keeps the DOM window bounded', () => {
    const w = computeWindow({ ...base, rowCount: 1_000_000, scrollTop: 123_456 })
    expect(w.rowEnd - w.rowStart).toBeLessThanOrEqual(Math.ceil(220 / 22) + 1 + 4)
    expect(w.colEnd - w.colStart).toBeLessThanOrEqual(Math.ceil(500 / 100) + 1 + 2)
  })
})

describe('column helpers', () => {
  it('builds prefix offsets', () => {
    expect(columnOffsets([10, 20, 30])).toEqual([0, 10, 30, 60])
    expect(columnOffsets([])).toEqual([0])
  })

  it('estimates widths within bounds', () => {
    const columns = [
      { name: 'ok', type: 'bool', numeric: false },
      { name: 'description', type: 'text', numeric: false },
      { name: 'n', type: 'int4', numeric: true },
      { name: 's', type: 'text', numeric: false },
    ]
    const [ok, desc, n, s] = estimateWidths(columns, [['t', 'x'.repeat(1000), '12345', '12345']])
    expect(ok).toBe(MIN_COL_PX)
    expect(desc).toBe(MAX_AUTO_COL_PX)
    // Numeric columns get headroom for longer values later in the result.
    expect(n).toBeGreaterThan(s)
  })

  it('widens the gutter with the row count', () => {
    expect(gutterWidth(10)).toBe(40)
    expect(gutterWidth(1_000_000)).toBeGreaterThan(gutterWidth(1000))
  })
})
