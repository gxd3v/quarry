// Plan trees from QueryService.Explain, flattened into rows for the plan view.

export interface PlanLike {
  op: string
  target: string
  cost: number
  rows: number
  actual: number // ms over all loops; -1 without analyze
  actRows: number // -1 without analyze
  props: Record<string, string>
  children: PlanLike[]
}

export interface PlanRow {
  id: string // path of child indexes, e.g. "0.1.0"
  depth: number
  node: PlanLike
  hasChildren: boolean
  // Time (analyzed) or cost spent in this node alone, without its children.
  self: number
  // self as a share of the whole plan, 0..1.
  share: number
  // actual/estimated rows when off by 10x or more either way, else 0.
  misestimate: number
}

// Estimates are "off" when actual and estimated rows differ by this factor.
export const MISESTIMATE = 10

export function analyzed(root: PlanLike): boolean {
  return root.actual >= 0
}

// selfOf is a node's own time (or cost) without its children's; planners
// report both cumulatively, so a parent's figure includes its children.
export function selfOf(n: PlanLike, useTime: boolean): number {
  const total = useTime ? n.actual : n.cost
  if (total <= 0) return 0
  let kids = 0
  for (const c of n.children) kids += Math.max(useTime ? c.actual : c.cost, 0)
  return Math.max(total - kids, 0)
}

export function misestimate(n: PlanLike): number {
  if (n.actRows < 0 || n.props['Never executed']) return 0
  const est = Math.max(n.rows, 1)
  const act = Math.max(n.actRows, 1)
  const r = act / est
  return r >= MISESTIMATE || r <= 1 / MISESTIMATE ? r : 0
}

// flatten lists the visible rows depth first, skipping the children of
// collapsed nodes (by id).
export function flatten(root: PlanLike, collapsed: ReadonlySet<string> = new Set()): PlanRow[] {
  const useTime = analyzed(root)
  let total = 0
  const sum = (n: PlanLike) => {
    total += selfOf(n, useTime)
    n.children.forEach(sum)
  }
  sum(root)
  const out: PlanRow[] = []
  const walk = (n: PlanLike, id: string, depth: number) => {
    const self = selfOf(n, useTime)
    out.push({ id, depth, node: n, hasChildren: n.children.length > 0, self, share: total > 0 ? self / total : 0, misestimate: misestimate(n) })
    if (!collapsed.has(id)) n.children.forEach((c, i) => walk(c, `${id}.${i}`, depth + 1))
  }
  walk(root, '0', 0)
  return out
}

// hottest returns the ids of the n nodes with the largest own share.
export function hottest(rows: readonly PlanRow[], n = 3): Set<string> {
  return new Set(
    [...rows]
      .filter((r) => r.share > 0.05)
      .sort((a, b) => b.share - a.share)
      .slice(0, n)
      .map((r) => r.id),
  )
}

// fmtMs renders a duration in ms with sensible precision.
export function fmtMs(ms: number): string {
  if (ms < 0) return ''
  if (ms < 1) return `${ms.toFixed(3)} ms`
  if (ms < 1000) return `${ms.toFixed(ms < 10 ? 2 : 1)} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

// fmtCount renders row counts and costs compactly (1.2k, 3.4M).
export function fmtCount(n: number): string {
  if (n < 0) return ''
  if (n < 1000) return Number.isInteger(n) ? String(n) : n.toFixed(2)
  if (n < 1e6) return `${(n / 1e3).toFixed(n < 1e4 ? 1 : 0)}k`
  if (n < 1e9) return `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)}M`
  return `${(n / 1e9).toFixed(1)}G`
}
