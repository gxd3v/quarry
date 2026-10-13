import type { Cell, GridColumn } from './buffer'

// Browsers cap element sizes near 33.5M px (WebKit and Blink LayoutUnit), so
// taller results are scrolled through a compressed, linearly mapped spacer.
export const MAX_SCROLL_PX = 30_000_000

export const CHAR_PX = 7.8
export const MIN_COL_PX = 48
export const MAX_AUTO_COL_PX = 360

export interface WindowInput {
  // Scroll position and size of the body area (excluding header and gutter).
  scrollTop: number
  scrollLeft: number
  width: number
  height: number
  rowCount: number
  rowHeight: number
  // Column start offsets plus the total width as the last entry.
  offsets: readonly number[]
  overscanRows: number
  overscanCols: number
}

export interface GridWindow {
  rowStart: number
  rowEnd: number
  // Physical top (in spacer pixels) of rowStart.
  rowTop: number
  colStart: number
  colEnd: number
}

export function spacerHeight(rowCount: number, rowHeight: number): number {
  return Math.min(rowCount * rowHeight, MAX_SCROLL_PX)
}

// scrollRatio maps physical scrollTop to virtual pixels (1 unless compressed).
export function scrollRatio(rowCount: number, rowHeight: number, height: number): number {
  const virtual = rowCount * rowHeight
  const maxScroll = spacerHeight(rowCount, rowHeight) - height
  return virtual > MAX_SCROLL_PX && maxScroll > 0 ? (virtual - height) / maxScroll : 1
}

export function computeWindow(w: WindowInput): GridWindow {
  const { rowCount, rowHeight: rh, height, offsets } = w

  const maxScroll = Math.max(0, spacerHeight(rowCount, rh) - height)
  const s = Math.min(Math.max(w.scrollTop, 0), maxScroll)
  const v = s * scrollRatio(rowCount, rh, height)
  const rowStart = Math.max(0, Math.floor(v / rh) - w.overscanRows)
  const rowEnd = Math.min(rowCount, Math.ceil((v + height) / rh) + w.overscanRows)

  const n = offsets.length - 1
  let colStart = 0
  let colEnd = 0
  if (n > 0) {
    const left = Math.max(w.scrollLeft, 0)
    const first = Math.min(Math.max(upperBound(offsets, left) - 1, 0), n - 1)
    colStart = Math.max(0, first - w.overscanCols)
    colEnd = Math.min(n, lowerBound(offsets, left + w.width) + w.overscanCols)
  }

  return { rowStart, rowEnd: Math.max(rowStart, rowEnd), rowTop: Math.round(rowStart * rh - v + s), colStart, colEnd }
}

export function columnOffsets(widths: readonly number[]): number[] {
  const out = new Array<number>(widths.length + 1)
  out[0] = 0
  for (let i = 0; i < widths.length; i++) out[i + 1] = out[i] + widths[i]
  return out
}

// estimateWidths sizes columns from their header and a sample of rows.
export function estimateWidths(columns: readonly GridColumn[], sample: readonly Cell[][]): number[] {
  return columns.map((col, c) => {
    let chars = Math.max(col.name.length, col.type.length * 0.85)
    for (const row of sample) chars = Math.max(chars, (row[c] ?? 'NULL').length)
    // Later numbers tend to be longer than the sampled ones, and clipped digits mislead.
    if (col.numeric) chars += 3
    return Math.round(Math.min(MAX_AUTO_COL_PX, Math.max(MIN_COL_PX, chars * CHAR_PX + 20)))
  })
}

export function gutterWidth(rowCount: number): number {
  return Math.ceil(Math.max(40, String(rowCount).length * CHAR_PX + 20))
}

// First index with a[i] >= x.
function lowerBound(a: readonly number[], x: number): number {
  let lo = 0
  let hi = a.length
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    if (a[mid] < x) lo = mid + 1
    else hi = mid
  }
  return lo
}

// First index with a[i] > x.
function upperBound(a: readonly number[], x: number): number {
  let lo = 0
  let hi = a.length
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    if (a[mid] <= x) lo = mid + 1
    else hi = mid
  }
  return lo
}
