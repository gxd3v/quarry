import { describe, expect, it } from 'vitest'
import { flatten, groupKey, nodeKey, orderConnections, type ChildrenState, type ConnEntry, type TreeNode } from './tree'

const conn = (id: string, group = '', favorite = false): ConnEntry => ({ id, name: id, env: 'dev', group, favorite })
const node = (path: string[], kind = 'table', leaf = false): TreeNode => ({ path, kind, name: path.at(-1)!, detail: '', leaf })

function source(children: Record<string, TreeNode[]>, expanded: string[]) {
  const map = new Map<string, ChildrenState>(Object.entries(children).map(([k, nodes]) => [k, { nodes, loading: false, error: '' }]))
  const open = new Set(expanded)
  return { children: (k: string) => map.get(k), expanded: (k: string) => open.has(k) }
}

const label = (r: ReturnType<typeof flatten>[number]) =>
  '  '.repeat(r.depth) + (r.type === 'group' ? `[${r.name}]` : r.type === 'conn' ? r.conn.name : r.node.name)

describe('orderConnections', () => {
  it('puts favorites first, then ungrouped, then groups', () => {
    const out = orderConnections([conn('b', 'g2'), conn('z'), conn('a', 'g1'), conn('f', 'g1', true), conn('c')])
    expect(out.map((g) => [g.group, g.conns.map((c) => c.id)])).toEqual([
      ['', ['f', 'c', 'z']],
      ['g1', ['a']],
      ['g2', ['b']],
    ])
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
    expect(rows.map(label)).toEqual(['local', '[work]', '  pg', '    app', '      public'])
    expect(flatten(conns, source(children, [])).map(label)).toEqual(['local', '[work]'])
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
