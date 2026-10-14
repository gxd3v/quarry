import { describe, expect, it } from 'vitest'
import { DEFAULT_LAYOUT } from './layout'
import { basename, encodeSession, isTablePath, restoreLayout, restoreTab, type StoredTab, type TabSnapshot } from './session'

const conn = 'c1'
const scope = { database: 'app', schema: 'public' }
const known = (id: string) => id === conn || id === 'c2'

describe('encodeSession', () => {
  it('stores unsaved content only for untitled and dirty tabs', () => {
    const tabs: TabSnapshot[] = [
      { path: '', title: 'Script 1', connId: conn, scope, cursor: 3, text: 'select 1', dirty: true },
      { path: 'C:\\s\\a.sql', title: 'a.sql', connId: conn, scope, cursor: 0, text: 'saved text', dirty: false },
      { path: '/s/b.sql', title: 'b.sql', connId: 'c2', scope, cursor: 2.6, text: 'edited', dirty: true },
    ]
    const s = encodeSession(tabs, 7, { explorer: 300, editor: 0.5, scratches: false })
    expect(s.tabs!.map((t) => [t.path, t.unsaved, t.dirty, t.cursor])).toEqual([
      ['', 'select 1', true, 3],
      ['C:\\s\\a.sql', '', false, 0],
      ['/s/b.sql', 'edited', true, 3],
    ])
    expect(s.active).toBe(2)
    expect(JSON.parse(s.layoutJson!)).toEqual({ explorer: 300, editor: 0.5, scratches: false })
    expect(encodeSession([], 3, DEFAULT_LAYOUT).active).toBe(0)
  })
})

describe('restoreTab', () => {
  const base: StoredTab = { path: '', title: 'Script 4', connId: conn, scope, cursor: 99, unsaved: 'select 2', dirty: true }

  it('restores untitled tabs from the session', () => {
    expect(restoreTab(base, null, known)).toEqual({
      path: '',
      title: 'Script 4',
      connId: conn,
      scope,
      text: 'select 2',
      saved: '',
      cursor: 8,
      scratch: false,
    })
    expect(restoreTab({ ...base, dirty: false }, null, known)?.saved).toBe('select 2')
    expect(restoreTab({ ...base, connId: 'gone' }, null, known)).toBeNull()
  })

  it('restores file tabs from disk, keeping unsaved edits', () => {
    const file = { text: 'on disk', scratch: true }
    const clean = restoreTab({ ...base, path: '/x/q.sql', dirty: false, unsaved: '', cursor: 3 }, file, known)!
    expect([clean.title, clean.text, clean.saved, clean.cursor, clean.scratch]).toEqual(['q.sql', 'on disk', 'on disk', 3, true])
    const dirty = restoreTab({ ...base, path: '/x/q.sql', unsaved: 'edited' }, file, known)!
    expect([dirty.text, dirty.saved]).toEqual(['edited', 'on disk'])
  })

  it('handles vanished files and missing connections', () => {
    expect(restoreTab({ ...base, path: '/x/q.sql', dirty: false, unsaved: '' }, null, known)).toBeNull()
    const kept = restoreTab({ ...base, path: '/x/q.sql', unsaved: 'mine' }, null, known)!
    expect([kept.text, kept.saved]).toEqual(['mine', ''])
    const rebound = restoreTab(
      { ...base, path: '/x/q.sql', connId: '', dirty: false },
      { text: 't', scratch: false, binding: { connId: 'c2', scope: { database: 'db2', schema: '' } } },
      known,
    )!
    expect([rebound.connId, rebound.scope]).toEqual(['c2', { database: 'db2', schema: '' }])
  })

  it('parses layouts and base names', () => {
    expect(restoreLayout('')).toEqual(DEFAULT_LAYOUT)
    expect(basename('C:\\Users\\me\\a.sql')).toBe('a.sql')
    expect(basename('/home/me/b.sql')).toBe('b.sql')
  })
})

describe('table tabs', () => {
  const table: TabSnapshot = {
    path: '',
    title: 'orders',
    connId: conn,
    scope,
    cursor: 0,
    text: '',
    dirty: false,
    table: {
      node: { path: ['app', 'public', 'tables', 'orders'], kind: 'table', name: 'orders' },
      filter: "status = 'open'",
      order: [{ column: 'id', dir: 'desc' }],
      view: 'columns',
    },
  }

  it('round-trips through a pseudo-path and JSON state', () => {
    const s = encodeSession([table], 0, DEFAULT_LAYOUT)
    const t = s.tabs![0]
    expect(t.path).toBe('quarry-table:["app","public","tables","orders"]')
    expect(isTablePath(t.path!)).toBe(true)
    expect(JSON.parse(t.unsaved!)).toEqual({ kind: 'table', name: 'orders', filter: "status = 'open'", order: [{ column: 'id', dir: 'desc' }], view: 'columns' })
    const stored: StoredTab = { path: t.path!, title: t.title!, connId: t.connId!, scope, cursor: 0, unsaved: t.unsaved!, dirty: false }
    const r = restoreTab(stored, null, known)!
    expect(r.path).toBe('')
    expect(r.title).toBe('orders')
    expect(r.table).toEqual(table.table)
  })

  it('drops malformed or orphaned table tabs', () => {
    const t: StoredTab = { path: 'quarry-table:["a","b","tables","t"]', title: 't', connId: conn, scope, cursor: 0, unsaved: '{}', dirty: false }
    expect(restoreTab(t, null, known)?.table).toEqual({ node: { path: ['a', 'b', 'tables', 't'], kind: 'table', name: 't' }, filter: '', order: [], view: 'data' })
    expect(restoreTab({ ...t, path: 'quarry-table:nope' }, null, known)).toBeNull()
    expect(restoreTab({ ...t, path: 'quarry-table:[]' }, null, known)).toBeNull()
    expect(restoreTab({ ...t, connId: 'gone' }, null, known)).toBeNull()
  })
})
