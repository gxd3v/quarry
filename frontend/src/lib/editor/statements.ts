export interface Range {
  start: number
  end: number
}

// statementAt picks the statement for a cursor offset like the backend does
// (postgres.StatementAt): the one containing it, else the previous one when
// the cursor sits on its last line, else the next one. -1 when there is none.
export function statementAt(ranges: readonly Range[], text: string, offset: number): number {
  if (ranges.length === 0) return -1
  offset = Math.min(Math.max(offset, 0), text.length)
  for (let i = 0; i < ranges.length; i++) {
    const r = ranges[i]
    if (offset < r.start) {
      if (i === 0) return 0
      const prevEnd = Math.min(ranges[i - 1].end, text.length)
      if (!text.slice(prevEnd, offset).includes('\n') || text.slice(offset, Math.min(r.start, text.length)).includes('\n')) {
        return i - 1
      }
      return i
    }
    if (offset <= r.end) return i
  }
  return ranges.length - 1
}
