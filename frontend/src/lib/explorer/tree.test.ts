import { describe, expect, it } from 'vitest'
import { dragName, flatten, folderTree, groupKey, groupParts, nodeKey, type ChildrenState, type ConnEntry, type TreeNode } from './tree'

const conn = (id: string, group = '', favorite = false, name = id): ConnEntry => ({ id, name, kind: 'postgres', env: 'dev', group, favorite })
const node = (path: string[], kind = 'table', leaf = false): TreeNode => ({ path, kind, name: path.at(-1)!, detail: '', leaf })

function source(children: Record<string, TreeNode[]>, expanded: string[]) {
  const map = new Map<string, ChildrenState>(Object.entries(children).map(([k, nodes]) => [k, { nodes, loading: false, error: '' }]))
  const open = new Set(expanded)
  return { children: (k: string) => map.get(k), expanded: (k: string) => open.has(k) }
}

const label = (r: ReturnType<typeof flatten>[number]) =>
  '  '.repeat(r.depth) + (r.type === 'group' ? `[${r.name}]` : r.type === 'conn' ? r.conn.name : r.node.name)

describe('folders', () => {
  it('nests folders and sorts every level', () => {
    const { favorites, root } = folderTree([
      conn('1', 'Iris/TST', false, 'postgres'),
      conn('2', 'Iris/PRD', false, 'postgres'),
      conn('3', 'Iris/PRD', false, 'oracle'),
      conn('4', 'acme'),
      conn('5', 'Acme', false, 'tst'),
      conn('6', '', true, 'fav'),
      conn('7', ''),
      conn('8', 'Item 10'),
      conn('9', 'Item 9'),
    ])
    expect(favorites.map((c) => c.name)).toEqual(['fav'])
    expect(root.folders.map((f) => f.path)).toEqual(['acme', 'Iris', 'Item 9', 'Item 10'])
    expect(root.folders[0].conns.map((c) => c.name)).toEqual(['4', 'tst'])
    const iris = root.folders[1]
    expect(iris.folders.map((f) => f.path)).toEqual(['Iris/PRD', 'Iris/TST'])
    expect(iris.folders[0].conns.map((c) => c.name)).toEqual(['oracle', 'postgres'])
    expect(root.conns.map((c) => c.id)).toEqual(['7'])
    expect(groupParts(' a / / b ')).toEqual(['a', 'b'])
  })

  it('shows open folders with counts', () => {
    const conns = [conn('p', 'Iris/PRD', false, 'postgres'), conn('t', 'Iris/TST', false, 'postgres'), conn('x')]
    const closed = flatten(conns, source({}, []))
    expect(closed.map(label)).toEqual(['[Iris]', 'x'])
    expect(closed[0]).toMatchObject({ type: 'group', count: 2, path: 'Iris' })
    const open = flatten(conns, source({}, [groupKey('Iris'), groupKey('Iris/PRD')]))
    expect(open.map(label)).toEqual(['[Iris]', '  [PRD]', '    postgres', '  [TST]', 'x'])
  })

  it('filters through folders', () => {
    const conns = [conn('p', 'Iris/PRD', false, 'postgres'), conn('o', 'Iris/TST', false, 'oracle'), conn('k', 'Kestrel', false, 'postgres')]
    expect(flatten(conns, source({}, []), 'orac').map(label)).toEqual(['[Iris]', '  [TST]', '    oracle'])
    // A matching folder shows everything inside it.
    expect(flatten(conns, source({}, []), 'iris').map(label)).toEqual(['[Iris]', '  [PRD]', '    postgres', '  [TST]', '    oracle'])
  })
})

describe('flatten', () => {
  const conns = [conn('pg', 'work'), conn('local')]
  const db = node(['app'], 'database')
  const schema = node(['app', 'public'], 'schema')
  const orders = node(['app', 'public', 'orders'], 'table', true)
  const users = node(['app', 'public', 'users'], 'table', true)
  const children = {
    pg: [db],
    [nodeKey('pg', ['app'])]: [schema],
    [nodeKey('pg', ['app', 'public'])]: [orders, users],
  }

  it('shows expanded nodes only', () => {
    const rows = flatten(conns, source(children, [groupKey('work'), 'pg', nodeKey('pg', ['app'])]))
    expect(rows.map(label)).toEqual(['[work]', '  pg', '    app', '      public', 'local'])
    expect(flatten(conns, source(children, [])).map(label)).toEqual(['[work]', 'local'])
  })

  it('filters loaded nodes and opens their ancestors', () => {
    const rows = flatten(conns, source(children, []), 'ORD')
    expect(rows.map(label)).toEqual(['[work]', '  pg', '    app', '      public', '        orders'])
    expect(flatten(conns, source(children, []), 'loc').map(label)).toEqual(['local'])
    expect(flatten(conns, source(children, []), 'nothing')).toEqual([])
  })

  it('carries loading and error state', () => {
    const src = source(children, ['pg', groupKey('work')])
    const k = nodeKey('pg', ['app'])
    const rows = flatten(conns, {
      ...src,
      children: (key) => (key === k ? { loading: true, error: 'boom' } : src.children(key)),
    })
    expect(rows.find((r) => r.key === k)).toMatchObject({ loading: true, error: 'boom', expanded: false })
  })
})

describe('dragName', () => {
  const node = (path: string[], kind: string, name = path[path.length - 1]) => ({ path, kind, name, detail: '', leaf: true })
  it('qualifies objects and quotes when needed', () => {
    expect(dragName(node(['app', 'public', 'tables', 'orders'], 'table'))).toBe('public.orders')
    expect(dragName(node(['app', 'Sales', 'views', 'Top Items'], 'view'))).toBe('"Sales"."Top Items"')
    expect(dragName(node(['app', 'public', 'functions', 'f(integer)'], 'function', 'f'))).toBe('public.f')
    expect(dragName(node(['app', 'public', 'tables', 'orders', 'columns', 'user'], 'column'))).toBe('"user"')
    expect(dragName(node(['app', 'public'], 'schema'))).toBe('public')
    expect(dragName(node(['app', 'public', 'tables'], 'folder', 'Tables'))).toBe('')
  })
})
