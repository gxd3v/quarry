import { SvelteMap, SvelteSet } from 'svelte/reactivity'
import { explorerClient } from '../api/clients'
import { errorMessage } from '../api/errors'
import { flatten, groupKey, groupParts, nodeKey, type ChildrenState, type ConnEntry, type Row, type TreeNode } from '../explorer/tree'
import { connections } from './connections.svelte'

export type { Row, TreeNode }
export { nodeKey }

const OPEN_GROUPS_KEY = 'quarry.openFolders'

function readOpenGroups(): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(OPEN_GROUPS_KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

class Explorer {
  filter = $state('')
  selected = $state('')
  readonly #children = new SvelteMap<string, ChildrenState>()
  readonly #expanded = new SvelteSet<string>()
  // Open folders, by path; folders start closed and remember their state.
  readonly #openGroups = new SvelteSet<string>(readOpenGroups())
  readonly #inflight = new Map<string, Promise<TreeNode[]>>()

  readonly entries: ConnEntry[] = $derived(
    connections.list.map((c) => {
      const cfg = c.config!
      return { id: cfg.id, name: cfg.name, kind: cfg.kind, env: cfg.env || 'local', group: cfg.group, favorite: cfg.favorite }
    }),
  )

  readonly rows: Row[] = $derived(
    flatten(
      this.entries,
      {
        children: (k) => this.#children.get(k),
        expanded: (k) => (k.startsWith(groupKey('')) ? this.#openGroups.has(k.slice(groupKey('').length)) : this.#expanded.has(k)),
      },
      this.filter,
    ),
  )

  children(key: string): ChildrenState | undefined {
    return this.#children.get(key)
  }

  // load returns the children of a node (the connection's roots for null),
  // from the cache unless refresh is set.
  load(connId: string, node: TreeNode | null, refresh = false): Promise<TreeNode[]> {
    const key = nodeKey(connId, node?.path)
    const cached = this.#children.get(key)
    if (cached?.nodes && !refresh) return Promise.resolve(cached.nodes)
    const running = this.#inflight.get(key)
    if (running) return running
    this.#children.set(key, { nodes: cached?.nodes, loading: true, error: '' })
    const p = connections
      .withConnection(connId, () => explorerClient().children({ connId, parent: node ?? undefined }))
      .then(
        (res) => {
          const nodes = res.nodes.map((n) => ({ path: n.path, kind: n.kind, name: n.name, detail: n.detail, leaf: n.leaf }))
          if (refresh) this.#dropBelow(key)
          this.#children.set(key, { nodes, loading: false, error: '' })
          if (refresh) this.#reloadExpanded()
          return nodes
        },
        (err: unknown) => {
          this.#children.set(key, { nodes: cached?.nodes, loading: false, error: errorMessage(err) })
          throw err
        },
      )
      .finally(() => this.#inflight.delete(key))
    this.#inflight.set(key, p)
    return p
  }

  isExpanded(key: string): boolean {
    return this.#expanded.has(key)
  }

  async expand(connId: string, node: TreeNode | null) {
    const key = nodeKey(connId, node?.path)
    this.#expanded.add(key)
    try {
      await this.load(connId, node)
      this.#reloadExpanded()
    } catch {
      // The error is shown on the node.
    }
  }

  collapse(key: string) {
    this.#expanded.delete(key)
  }

  toggleGroup(path: string, open = !this.#openGroups.has(path)) {
    if (open) this.#openGroups.add(path)
    else this.#openGroups.delete(path)
    try {
      localStorage.setItem(OPEN_GROUPS_KEY, JSON.stringify([...this.#openGroups]))
    } catch {
      // storage may be unavailable
    }
  }

  // reveal opens the folders that lead to a connection.
  reveal(group: string) {
    const parts = groupParts(group)
    for (let i = 1; i <= parts.length; i++) this.toggleGroup(parts.slice(0, i).join('/'), true)
  }

  async refresh(connId: string, node: TreeNode | null) {
    try {
      await this.load(connId, node, true)
    } catch {
      // Shown on the node.
    }
  }

  // forget drops everything cached for a connection and collapses it.
  forget(connId: string) {
    for (const k of [...this.#children.keys()]) if (k === connId || k.startsWith(connId + '\u0001')) this.#children.delete(k)
    for (const k of [...this.#expanded]) if (k === connId || k.startsWith(connId + '\u0001')) this.#expanded.delete(k)
  }

  // Expanded nodes whose children were dropped load them again.
  #reloadExpanded() {
    for (const r of this.rows) {
      if (r.type === 'node' && r.expanded && !this.#children.has(r.key)) this.expand(r.connId, r.node)
    }
  }

  #dropBelow(key: string) {
    const prefix = key + '\u0001'
    for (const k of [...this.#children.keys()]) if (k.startsWith(prefix)) this.#children.delete(k)
  }
}

export const explorer = new Explorer()
