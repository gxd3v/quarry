// Flattening of the explorer tree into visible rows. Pure, so the store can
// stay a thin reactive wrapper.

export interface TreeNode {
  path: string[]
  kind: string
  name: string
  detail: string
  leaf: boolean
}

export interface ChildrenState {
  nodes?: TreeNode[]
  loading: boolean
  error: string
}

export interface ConnEntry {
  id: string
  name: string
  env: string
  group: string
  favorite: boolean
}

interface RowBase {
  key: string
  depth: number
  expanded: boolean
}

export type Row =
  | (RowBase & { type: 'group'; name: string })
  | (RowBase & { type: 'conn'; conn: ConnEntry; loading: boolean; error: string })
  | (RowBase & { type: 'node'; connId: string; node: TreeNode; loading: boolean; error: string })

export interface TreeSource {
  children(key: string): ChildrenState | undefined
  expanded(key: string): boolean
}

const SEP = '\u0001'

// nodeKey identifies a node; a connection's own key is its id.
export function nodeKey(connId: string, path: readonly string[] = []): string {
  return path.length ? connId + SEP + path.join(SEP) : connId
}

export function groupKey(name: string): string {
  return 'group' + SEP + name
}

// orderConnections puts favorites first, then ungrouped connections, then
// groups, each by name.
export function orderConnections(conns: readonly ConnEntry[]): { group: string; conns: ConnEntry[] }[] {
  const byName = (a: ConnEntry, b: ConnEntry) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
  const favorites = conns.filter((c) => c.favorite).sort(byName)
  const groups = new Map<string, ConnEntry[]>()
  for (const c of conns) {
    if (c.favorite) continue
    const list = groups.get(c.group) ?? []
    list.push(c)
    groups.set(c.group, list)
  }
  const out: { group: string; conns: ConnEntry[] }[] = []
  if (favorites.length) out.push({ group: '', conns: favorites })
  const names = [...groups.keys()].sort((a, b) => (a === '' ? -1 : b === '' ? 1 : a.localeCompare(b)))
  for (const g of names) {
    const list = groups.get(g)!.sort(byName)
    if (g === '' && out.length) out[0].conns.push(...list)
    else out.push({ group: g, conns: list })
  }
  return out
}

export function flatten(conns: readonly ConnEntry[], src: TreeSource, filter = ''): Row[] {
  const q = filter.trim().toLowerCase()
  const hit = (name: string) => name.toLowerCase().includes(q)
  const memo = new Map<string, boolean>()

  // Whether a loaded descendant of key matches the filter.
  const below = (connId: string, key: string): boolean => {
    let v = memo.get(key)
    if (v === undefined) {
      v = (src.children(key)?.nodes ?? []).some((n) => hit(n.name) || below(connId, nodeKey(connId, n.path)))
      memo.set(key, v)
    }
    return v
  }

  const rows: Row[] = []
  const visit = (connId: string, key: string, depth: number) => {
    const state = src.children(key)
    for (const n of state?.nodes ?? []) {
      const k = nodeKey(connId, n.path)
      const deep = q !== '' && below(connId, k)
      if (q && !hit(n.name) && !deep) continue
      const child = src.children(k)
      const expanded = !n.leaf && (q ? deep || (src.expanded(k) && hit(n.name)) : src.expanded(k))
      rows.push({
        type: 'node',
        key: k,
        depth,
        expanded,
        connId,
        node: n,
        loading: child?.loading ?? false,
        error: child?.error ?? '',
      })
      if (expanded) visit(connId, k, depth + 1)
    }
  }

  for (const { group, conns: list } of orderConnections(conns)) {
    let depth = 0
    if (group) {
      const gk = groupKey(group)
      const visible = list.filter((c) => !q || hit(c.name) || below(c.id, c.id))
      if (q && !visible.length) continue
      const open = q !== '' || src.expanded(gk)
      rows.push({ type: 'group', key: gk, depth: 0, expanded: open, name: group })
      if (!open) continue
      depth = 1
    }
    for (const c of list) {
      const deep = q !== '' && below(c.id, c.id)
      if (q && !hit(c.name) && !deep) continue
      const st = src.children(c.id)
      const expanded = q ? deep : src.expanded(c.id)
      rows.push({ type: 'conn', key: c.id, depth, expanded, conn: c, loading: st?.loading ?? false, error: st?.error ?? '' })
      if (expanded) visit(c.id, c.id, depth + 1)
    }
  }
  return rows
}
