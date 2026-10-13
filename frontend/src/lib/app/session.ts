// The restorable workspace: open tabs, the active one and the layout, as
// WorkspaceService.SaveSession stores them. Pure, so it can be tested.
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { SessionSchema } from '../gen/quarry/v1/workspace_pb'
import type { ScopeLike } from '../sql'
import { clampLayout, parseLayout, type Layout } from './layout'

export interface TabSnapshot {
  // File or scratch path; '' for untitled tabs.
  path: string
  title: string
  connId: string
  scope: ScopeLike
  cursor: number
  text: string
  dirty: boolean
}

export function encodeSession(tabs: readonly TabSnapshot[], active: number, layout: Layout): MessageInitShape<typeof SessionSchema> {
  return {
    tabs: tabs.map((t) => ({
      path: t.path,
      title: t.title,
      connId: t.connId,
      scope: { database: t.scope.database, schema: t.scope.schema },
      cursor: Math.max(0, Math.round(t.cursor)),
      // Files keep their content on disk; only what is not saved goes here.
      unsaved: !t.path || t.dirty ? t.text : '',
      dirty: t.dirty,
    })),
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
