// Script files, scratches and the restored session (WorkspaceService).
import { create } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { workspaceClient } from '../api/clients'
import { errorCode } from '../api/errors'
import { openScriptDialog, saveScriptDialog } from '../api/server'
import { SessionSchema, type FileInfo, type ReadFileResponse } from '../gen/quarry/v1/workspace_pb'
import type { ScopeLike } from '../sql'
import { connections } from './connections.svelte'
import { parseLayout } from './layout'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import { basename, encodeSession, isTablePath, restoreTab, type FileRead, type TabSnapshot } from './session'
import { openTable } from './table.svelte'
import { tabs, type Tab } from './tabs.svelte'
import { ui } from './ui.svelte'

const SAVE_DELAY_MS = 1000
// Panel sizes lived in localStorage before they moved into the session.
const LEGACY_LAYOUT_KEY = 'quarry.layout'

function toFileRead(res: ReadFileResponse): FileRead {
  return {
    text: res.text,
    scratch: res.scratch,
    binding: res.binding ? { connId: res.binding.connId, scope: res.binding.scope } : undefined,
  }
}

class Workspace {
  scratches = $state.raw<FileInfo[]>([])
  // Set once the session was restored; saving starts then.
  ready = $state(false)
  #timer: ReturnType<typeof setTimeout> | undefined
  #pending = false
  #saving: Promise<void> | null = null

  // --- scratches and files ---

  async loadScratches() {
    try {
      const res = await workspaceClient().listScratches({})
      this.scratches = res.files
    } catch (err) {
      notify.error(err, 'List scratches')
    }
  }

  // newScratch creates a scratch bound to the active tab's connection (or a
  // picked one) and opens it.
  async newScratch(connId?: string, scope?: ScopeLike): Promise<Tab | null> {
    const base = tabs.active
    connId ??= base?.connId ?? (await ui.pickConnection('New scratch on…')) ?? undefined
    if (!connId) return null
    scope ??= base && base.connId === connId ? { ...base.scope } : { database: connections.config(connId)?.database ?? '', schema: '' }
    const ws = workspaceClient()
    const { path } = await ws.newScratch({ ext: '.sql' })
    await ws.bind({ path, binding: { connId, scope } })
    const tab = tabs.open(connId, scope, '', { path, title: basename(path), scratch: true, saved: '' })
    void this.loadScratches()
    return tab
  }

  async openFile(): Promise<Tab | null> {
    const path = await openScriptDialog()
    return path ? this.openPath(path) : null
  }

  // openPath activates the tab showing path, or opens the file in a new tab
  // bound to its connection, asking for one when it has none.
  async openPath(path: string): Promise<Tab | null> {
    const open = tabs.byPath(path)
    if (open) {
      tabs.activate(open)
      return open
    }
    const res = await workspaceClient().readFile({ path })
    let connId = res.binding?.connId ?? ''
    let scope: ScopeLike = { database: res.binding?.scope?.database ?? '', schema: res.binding?.scope?.schema ?? '' }
    if (!connId || !connections.get(connId)) {
      const picked = await ui.pickConnection(`Connection for ${basename(path)}`)
      if (!picked) return null
      connId = picked
      scope = { database: connections.config(picked)?.database ?? '', schema: '' }
      await workspaceClient().bind({ path, binding: { connId, scope } })
    }
    return tabs.open(connId, scope, res.text, { path, title: basename(path), scratch: res.scratch, saved: res.text })
  }

  // save writes the tab to its file, asking where for untitled tabs. It
  // returns false when nothing was saved.
  async save(tab: Tab): Promise<boolean> {
    if (!tab.path) return this.saveAs(tab)
    const doc = tabs.state(tab).doc
    await workspaceClient().writeFile({ path: tab.path, text: doc.toString() })
    tabs.markSaved(tab, doc)
    notify.flash(`Saved ${tab.title}`)
    if (tab.scratch) void this.loadScratches()
    return true
  }

  async saveAs(tab: Tab): Promise<boolean> {
    const name = tab.path ? basename(tab.path) : `${tab.title.replace(/[\\/:*?"<>|]+/g, '_')}.sql`
    const path = await saveScriptDialog(name)
    if (!path) return false
    const doc = tabs.state(tab).doc
    const ws = workspaceClient()
    await ws.writeFile({ path, text: doc.toString() })
    await ws.bind({ path, binding: { connId: tab.connId, scope: { ...tab.scope } } })
    tab.path = path
    tab.title = basename(path)
    tab.scratch = false
    tabs.markSaved(tab, doc)
    notify.flash(`Saved ${tab.title}`)
    return true
  }

  // closeTab closes a tab, offering to save unsaved changes first; false
  // when the user kept it open.
  async closeTab(tab: Tab): Promise<boolean> {
    const pending = tab.table?.changes ?? 0
    if (pending) {
      tabs.activate(tab)
      const ok = await prompts.ask({
        title: 'Pending changes',
        text: `${tab.title} has ${pending} pending ${pending === 1 ? 'change' : 'changes'} that were not applied. Close and discard them?`,
        confirm: 'Discard and close',
        danger: true,
      })
      if (!ok) return false
    }
    if (tab.dirty) {
      tabs.activate(tab)
      const choice = await prompts.choose({
        title: 'Unsaved changes',
        text: `Save the changes to ${tab.title}?`,
        choices: [
          { value: 'cancel', label: 'Cancel' },
          { value: 'discard', label: "Don't save", danger: true },
          { value: 'save', label: 'Save', primary: true },
        ],
      })
      if (choice === null || choice === 'cancel') return false
      if (choice === 'save') {
        try {
          if (!(await this.save(tab))) return false
        } catch (err) {
          notify.error(err, `Save ${tab.title}`)
          return false
        }
      }
    }
    await tabs.close(tab)
    return !tabs.tabs.includes(tab)
  }

  async deleteScratch(f: FileInfo) {
    const open = tabs.byPath(f.path)
    const ok = await prompts.ask({
      title: 'Delete scratch',
      text: `Delete ${f.name}?` + (open ? ' Its tab is closed.' : ''),
      confirm: 'Delete',
      danger: true,
    })
    if (!ok) return
    if (open) await tabs.close(open)
    if (open && tabs.tabs.includes(open)) return
    try {
      await workspaceClient().deleteScratch({ path: f.path })
    } catch (err) {
      notify.error(err, `Delete ${f.name}`)
    }
    await this.loadScratches()
  }

  // --- session ---

  // restore reopens the saved tabs and layout. Saving starts afterwards, so a
  // failed restore never overwrites the stored session.
  async restore() {
    let session
    try {
      session = (await workspaceClient().loadSession({})).session
    } catch (err) {
      notify.error(err, 'Restore session')
      return
    }
    ui.layout = parseLayout(session?.layoutJson || legacyLayout())
    const stored = session?.tabs ?? []
    const files = await Promise.all(
      stored.map((t) =>
        t.path && !isTablePath(t.path)
          ? workspaceClient()
              .readFile({ path: t.path })
              .then(toFileRead)
              .catch((err: unknown) => {
                if (errorCode(err) !== Code.NotFound) notify.error(err, `Open ${basename(t.path)}`)
                return null
              })
          : Promise.resolve(null),
      ),
    )
    const known = (id: string) => !!connections.get(id)
    let active: Tab | null = null
    for (let i = 0; i < stored.length; i++) {
      const r = restoreTab(stored[i], files[i], known)
      if (!r) continue
      const tab = r.table
        ? openTable(r.connId, r.table.node, r.table, r.title)
        : tabs.open(r.connId, r.scope, r.text, { path: r.path, title: r.title, scratch: r.scratch, saved: r.saved, cursor: r.cursor })
      if (i === session?.active) active = tab
    }
    if (active) tabs.activate(active)
    else if (tabs.tabs.length) tabs.activate(tabs.tabs[tabs.tabs.length - 1])
    this.ready = true
  }

  // schedule saves the session shortly, coalescing bursts of changes.
  schedule() {
    if (!this.ready) return
    this.#pending = true
    clearTimeout(this.#timer)
    this.#timer = setTimeout(() => void this.flush(), SAVE_DELAY_MS)
  }

  // flush saves now if anything changed since the last save.
  async flush() {
    clearTimeout(this.#timer)
    if (!this.ready || !this.#pending) return this.#saving ?? undefined
    this.#pending = false
    const snapshot = tabs.tabs.map((t): TabSnapshot => {
      const st = tabs.state(t)
      const keep = !t.path || t.dirty
      return {
        path: t.path,
        title: t.title,
        connId: t.connId,
        scope: { ...t.scope },
        cursor: st.selection.main.head,
        text: keep && !t.table ? st.doc.toString() : '',
        dirty: t.dirty,
        table: t.table?.snapshot(),
      }
    })
    const active = tabs.tabs.findIndex((t) => t.key === tabs.activeKey)
    const session = create(SessionSchema, encodeSession(snapshot, active, ui.layout))
    const p = workspaceClient()
      .saveSession({ session })
      .then(
        () => {},
        (err: unknown) => {
          this.#pending = true
          notify.error(err, 'Save session')
        },
      )
      .finally(() => {
        if (this.#saving === p) this.#saving = null
      })
    this.#saving = p
    return p
  }
}

function legacyLayout(): string {
  try {
    const raw = localStorage.getItem(LEGACY_LAYOUT_KEY) ?? ''
    if (raw) localStorage.removeItem(LEGACY_LAYOUT_KEY)
    return raw
  } catch {
    return ''
  }
}

export const workspace = new Workspace()
