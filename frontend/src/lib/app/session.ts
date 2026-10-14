// The restorable workspace: open tabs, the active one and the layout, as
// WorkspaceService.SaveSession stores them. Pure, so it can be tested.
//
// Table tabs have no file. They are stored with the pseudo-path
// "quarry-table:" followed by the JSON array of the explorer node path, and
// their view state as JSON in `unsaved`: {"kind","name","filter","order","view"}.
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { SessionSchema } from '../gen/quarry/v1/workspace_pb'
import type { ScopeLike } from '../sql'
import { parseOrder, type OrderTerm } from '../table/order'
import { clampLayout, parseLayout, type Layout } from './layout'

export const TABLE_PATH = 'quarry-table:'

export interface TableNodeRef {
  path: string[]
  kind: string
  name: string
}

export interface TableSnapshot {
  node: TableNodeRef
  filter: string
  order: OrderTerm[]
  // The sub-tab shown: data, columns, indexes, …
  view: string
}

export function isTablePath(path: string): boolean {
  return path.startsWith(TABLE_PATH)
}

export interface TabSnapshot {
  // File or scratch path; '' for untitled tabs.
  path: string
  title: string
  connId: string
  scope: ScopeLike
  cursor: number
  text: string
  dirty: boolean
  table?: TableSnapshot
}

export function encodeSession(tabs: readonly TabSnapshot[], active: number, layout: Layout): MessageInitShape<typeof SessionSchema> {
  return {
    tabs: tabs.map((t) => {
      if (t.table) {
        const { node, filter, order, view } = t.table
        return {
          path: TABLE_PATH + JSON.stringify(node.path),
          title: t.title,
          connId: t.connId,
          scope: { database: t.scope.database, schema: t.scope.schema },
          cursor: 0,
          unsaved: JSON.stringify({ kind: node.kind, name: node.name, filter, order, view }),
          dirty: false,
        }
      }
      return {
        path: t.path,
        title: t.title,
        connId: t.connId,
        scope: { database: t.scope.database, schema: t.scope.schema },
        cursor: Math.max(0, Math.round(t.cursor)),
        // Files keep their content on disk; only what is not saved goes here.
        unsaved: !t.path || t.dirty ? t.text : '',
        dirty: t.dirty,
      }
    }),
    active: tabs.length ? Math.min(Math.max(active, 0), tabs.length - 1) : 0,
    layoutJson: JSON.stringify(clampLayout(layout)),
  }
}

export interface StoredTab {
  path: string
  title: string
  connId: string
  scope?: { database: string; schema: string }
  cursor: number
  unsaved: string
  dirty: boolean
}

export interface FileRead {
  text: string
  binding?: { connId: string; scope?: { database: string; schema: string } }
  scratch: boolean
}

export interface RestoredTab {
  path: string
  title: string
  connId: string
  scope: ScopeLike
  // Editor content, and the content last saved (dirty when they differ).
  text: string
  saved: string
  cursor: number
  scratch: boolean
  table?: TableSnapshot
}

// decodeTable reads a stored table tab, or null when it is malformed.
export function decodeTable(t: Pick<StoredTab, 'path' | 'unsaved'>): TableSnapshot | null {
  try {
    const path: unknown = JSON.parse(t.path.slice(TABLE_PATH.length))
    const v = JSON.parse(t.unsaved || '{}') as Record<string, unknown>
    if (!Array.isArray(path) || !path.length || !path.every((p) => typeof p === 'string')) return null
    if (!v || typeof v !== 'object') return null
    const str = (x: unknown, d = '') => (typeof x === 'string' ? x : d)
    return {
      node: { path, kind: str(v.kind, 'table'), name: str(v.name, path[path.length - 1]) },
      filter: str(v.filter),
      order: parseOrder(v.order),
      view: str(v.view, 'data'),
    }
  } catch {
    return null
  }
}

// restoreTab rebuilds a tab from the session and, for file tabs, the file as
// read now (null when it could not be read). It returns null for tabs that
// cannot come back: no connection, or a vanished file with nothing unsaved.
// known tells whether a connection still exists.
export function restoreTab(t: StoredTab, file: FileRead | null, known: (connId: string) => boolean): RestoredTab | null {
  let connId = t.connId
  let scope: ScopeLike = { database: t.scope?.database ?? '', schema: t.scope?.schema ?? '' }
  if ((!connId || !known(connId)) && file?.binding && known(file.binding.connId)) {
    connId = file.binding.connId
    scope = { database: file.binding.scope?.database ?? '', schema: file.binding.scope?.schema ?? '' }
  }
  if (!connId || !known(connId)) return null

  if (isTablePath(t.path)) {
    const table = decodeTable(t)
    if (!table) return null
    return { path: '', title: t.title || table.node.name, connId, scope, text: '', saved: '', cursor: 0, scratch: false, table }
  }

  let text: string
  let saved: string
  if (t.path) {
    if (file) {
      saved = file.text
      text = t.dirty ? t.unsaved : file.text
    } else {
      if (!t.dirty || !t.unsaved) return null
      saved = ''
      text = t.unsaved
    }
  } else {
    text = t.unsaved
    // An untitled tab that was clean (e.g. opened from the explorer) stays clean.
    saved = t.dirty ? '' : t.unsaved
  }
  return {
    path: t.path,
    title: t.path ? basename(t.path) : t.title,
    connId,
    scope,
    text,
    saved,
    cursor: Math.min(Math.max(0, t.cursor), text.length),
    scratch: file?.scratch ?? false,
  }
}

export function restoreLayout(json: string): Layout {
  return parseLayout(json)
}

export function basename(path: string): string {
  return path.split(/[\\/]/).pop() ?? path
}
