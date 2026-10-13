// Panel layout, saved with the session (Session.layout_json).
export interface Layout {
  // Explorer width in px.
  explorer: number
  // Editor share of the center column's height.
  editor: number
  // Whether the explorer's Scratches section is expanded.
  scratches: boolean
}

export const DEFAULT_LAYOUT: Layout = { explorer: 280, editor: 0.45, scratches: true }
export const EXPLORER_MIN = 160
export const EXPLORER_MAX = 720

export function clampLayout(l: Layout): Layout {
  const explorer = Number.isFinite(l.explorer) ? l.explorer : DEFAULT_LAYOUT.explorer
  const editor = Number.isFinite(l.editor) ? l.editor : DEFAULT_LAYOUT.editor
  return {
    explorer: Math.round(Math.min(EXPLORER_MAX, Math.max(EXPLORER_MIN, explorer))),
    editor: Math.min(0.9, Math.max(0.1, editor)),
    scratches: typeof l.scratches === 'boolean' ? l.scratches : DEFAULT_LAYOUT.scratches,
  }
}

export function parseLayout(raw: string | null | undefined): Layout {
  if (!raw) return { ...DEFAULT_LAYOUT }
  try {
    const v = JSON.parse(raw) as Partial<Layout>
    if (!v || typeof v !== 'object') return { ...DEFAULT_LAYOUT }
    return clampLayout({ ...DEFAULT_LAYOUT, ...v })
  } catch {
    return { ...DEFAULT_LAYOUT }
  }
}

// cycle returns the index dir steps from i in a ring of n items.
export function cycle(i: number, n: number, dir: number): number {
  return n === 0 ? -1 : (((i + dir) % n) + n) % n
}

// afterClose picks the index to activate when the item at i of n closes:
// the next one, or the previous when it was the last.
export function afterClose(i: number, n: number): number {
  if (n <= 1) return -1
  return i < n - 1 ? i : i - 1
}
