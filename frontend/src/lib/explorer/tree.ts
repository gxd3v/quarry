// Flattening of the explorer tree into visible rows. Pure, so the store can
// stay a thin reactive wrapper.
import { quoteIdent, schemaOfObject } from '../sql'

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
  kind: string
  env: string
  // Folder path, nested with "/" (e.g. "Acme/PRD"); '' at the top level.
  group: string
  favorite: boolean
}

interface RowBase {
  key: string
  depth: number
  expanded: boolean
}

export type Row =
  | (RowBase & { type: 'group'; name: string; path: string; count: number })
  | (RowBase & { type: 'conn'; conn: ConnEntry; loading: boolean; error: string })
  | (RowBase & { type: 'node'; connId: string; node: TreeNode; loading: boolean; error: string })

export interface TreeSource {
  children(key: string): ChildrenState | undefined
  expanded(key: string): boolean
}

const SEP = ''

// nodeKey identifies a node; a connection's own key is its id.
export function nodeKey(connId: string, path: readonly string[] = []): string {
  return path.length ? connId + SEP + path.join(SEP) : connId
}

export function groupKey(path: string): string {
  return 'group' + SEP + path
}

// groupParts splits a folder path, ignoring blank parts.
export function groupParts(group: string): string[] {
  return group
    .split('/')
    .map((p) => p.trim())
    .filter(Boolean)
}

// Folder is a node of the connection folders: subfolders and connections.
export interface Folder {
  name: string
  path: string
  folders: Folder[]
  conns: ConnEntry[]
}

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true })

// folderTree arranges connections in their folders, each level sorted by
// name; favorites are listed apart, at the top.
export function folderTree(conns: readonly ConnEntry[]): { favorites: ConnEntry[]; root: Folder } {
  const root: Folder = { name: '', path: '', folders: [], conns: [] }
  const favorites: ConnEntry[] = []
  for (const c of conns) {
    if (c.favorite) {
      favorites.push(c)
      continue
    }
    let f = root
    for (const part of groupParts(c.group)) {
      let next = f.folders.find((x) => x.name.toLowerCase() === part.toLowerCase())
      if (!next) {
        next = { name: part, path: f.path ? `${f.path}/${part}` : part, folders: [], conns: [] }
        f.folders.push(next)
      }
      f = next
    }
    f.conns.push(c)
  }
  const sort = (f: Folder) => {
    f.folders.sort(byName)
    f.conns.sort(byName)
    f.folders.forEach(sort)
  }
  sort(root)
  favorites.sort(byName)
  return { favorites, root }
}

function countConns(f: Folder): number {
  return f.conns.length + f.folders.reduce((n, x) => n + countConns(x), 0)
}

// flatten lists the visible rows: favorites, then folders (folders first at
// every level), then connections and their expanded objects. A filter opens
// whatever leads to a match among names and loaded objects.
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
  const connMatches = (c: ConnEntry) => !q || hit(c.name) || below(c.id, c.id)
  const folderMatches = (f: Folder): boolean => hit(f.name) || f.conns.some(connMatches) || f.folders.some(folderMatches)

  const rows: Row[] = []
  const visit = (connId: string, key: string, depth: number) => {
    const state = src.children(key)
    for (const n of state?.nodes ?? []) {
      const k = nodeKey(connId, n.path)
      const deep = q !== '' && below(connId, k)
      if (q && !hit(n.name) && !deep) continue
      const child = src.children(k)
      const expanded = !n.leaf && (q ? deep || (src.expanded(k) && hit(n.name)) : src.expanded(k))
      rows.push({ type: 'node', key: k, depth, expanded, connId, node: n, loading: child?.loading ?? false, error: child?.error ?? '' })
      if (expanded) visit(connId, k, depth + 1)
    }
  }
  const addConn = (c: ConnEntry, depth: number, all: boolean) => {
    const deep = q !== '' && below(c.id, c.id)
    if (q && !all && !hit(c.name) && !deep) return
    const st = src.children(c.id)
    const expanded = q ? deep : src.expanded(c.id)
    rows.push({ type: 'conn', key: c.id, depth, expanded, conn: c, loading: st?.loading ?? false, error: st?.error ?? '' })
    if (expanded) visit(c.id, c.id, depth + 1)
  }
  // all: the folder name matched, so everything inside shows.
  const addFolder = (f: Folder, depth: number, all: boolean) => {
    if (q && !all && !folderMatches(f)) return
    const k = groupKey(f.path)
    const inside = all || (q !== '' && hit(f.name))
    const open = q ? true : src.expanded(k)
    rows.push({ type: 'group', key: k, depth, expanded: open, name: f.name, path: f.path, count: countConns(f) })
    if (!open) return
    for (const sub of f.folders) addFolder(sub, depth + 1, inside)
    for (const c of f.conns) addConn(c, depth + 1, inside)
  }

  const { favorites, root } = folderTree(conns)
  for (const c of favorites) addConn(c, 0, false)
  for (const f of root.folders) addFolder(f, 0, false)
  for (const c of root.conns) addConn(c, 0, false)
  return rows
}

const QUALIFIED = new Set(['table', 'view', 'sequence', 'function', 'collection'])
const BARE = new Set(['database', 'schema', 'column', 'index', 'key'])

// dragName is the text a node inserts when dropped into the editor: the
// schema-qualified name of objects, the plain name of columns and the like,
// quoted where PostgreSQL needs it. '' when the node is not draggable.
export function dragName(n: TreeNode, dbKind = 'postgres'): string {
  if (QUALIFIED.has(n.kind)) {
    const schema = schemaOfObject(n.path, dbKind)
    return schema ? `${quoteIdent(schema)}.${quoteIdent(n.name)}` : quoteIdent(n.name)
  }
  return BARE.has(n.kind) ? quoteIdent(n.name) : ''
}
