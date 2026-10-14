// Server-side ordering of a table view: a list of columns, each ascending or
// descending, in priority order.

export type SortDir = 'asc' | 'desc'

export interface OrderTerm {
  column: string
  dir: SortDir
}

const next: Record<SortDir | 'none', SortDir | null> = { none: 'asc', asc: 'desc', desc: null }

// cycleOrder applies a click on a column header: asc → desc → none. A plain
// click orders by that column alone; with add (Shift) it changes the column
// in place, appending it when absent, and keeps the other terms.
export function cycleOrder(order: readonly OrderTerm[], column: string, add: boolean): OrderTerm[] {
  const i = order.findIndex((t) => t.column === column)
  const dir = next[i < 0 ? 'none' : order[i].dir]
  if (!add) return dir ? [{ column, dir }] : []
  if (i < 0) return [...order, { column, dir: 'asc' }]
  if (!dir) return order.filter((_, j) => j !== i)
  return order.map((t, j) => (j === i ? { column, dir } : t))
}

// orderBy renders terms for OpenData: a name, quoted in SQL form when it has
// whitespace or quotes, optionally followed by " DESC".
export function orderBy(order: readonly OrderTerm[]): string[] {
  return order.map((t) => {
    const name = /[\s"]/.test(t.column) ? `"${t.column.replaceAll('"', '""')}"` : t.column
    return t.dir === 'desc' ? `${name} DESC` : name
  })
}

// sortOf returns a column's direction and 1-based rank, or null.
export function sortOf(order: readonly OrderTerm[], column: string): { dir: SortDir; rank: number } | null {
  const i = order.findIndex((t) => t.column === column)
  return i < 0 ? null : { dir: order[i].dir, rank: i + 1 }
}

// parseOrder validates terms restored from storage.
export function parseOrder(raw: unknown): OrderTerm[] {
  if (!Array.isArray(raw)) return []
  return raw.flatMap((t: unknown) => {
    if (!t || typeof t !== 'object') return []
    const { column, dir } = t as Record<string, unknown>
    return typeof column === 'string' && column && (dir === 'asc' || dir === 'desc') ? [{ column, dir }] : []
  })
}
