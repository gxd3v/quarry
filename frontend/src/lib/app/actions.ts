// User actions that span several stores, shared by the explorer, menus and
// the keymap.
import { clone } from '@bufbuild/protobuf'
import { EditorView } from '@codemirror/view'
import { ConnectionConfigSchema, type ConnectionConfig } from '../gen/quarry/v1/connection_pb'
import { RunMode } from '../gen/quarry/v1/query_pb'
import { scopeOfPath, selectAll, type ScopeLike } from '../sql'
import { connections } from './connections.svelte'
import { explorer, type TreeNode } from './explorer.svelte'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import { editorHost, tabs, type Tab } from './tabs.svelte'
import { ui } from './ui.svelte'
import { workspace } from './workspace.svelte'

export async function connect(id: string) {
  try {
    await connections.ensure(id)
  } catch (err) {
    notify.error(err, `Connect ${connections.name(id)}`)
    throw err
  }
}

export async function disconnect(id: string) {
  try {
    await connections.disconnect(id)
    explorer.forget(id)
  } catch (err) {
    notify.error(err, `Disconnect ${connections.name(id)}`)
  }
}

export async function connectAndExpand(id: string) {
  explorer.selected = id
  try {
    await connect(id)
  } catch {
    return
  }
  await explorer.expand(id, null)
}

export function newScript(connId: string, scope?: ScopeLike, text = '', title = ''): Tab {
  const cfg = connections.config(connId)
  return tabs.open(connId, scope ?? { database: cfg?.database ?? '', schema: '' }, text, { title })
}

// pickAndNewScript asks for a connection and opens an untitled script on it.
export async function pickAndNewScript() {
  const id = await ui.pickConnection('New script on…')
  if (id) newScript(id)
}

// openObject opens a new script that selects from a table or view, and runs it.
export function openObject(connId: string, node: Pick<TreeNode, 'path' | 'name'>) {
  const scope = scopeOfPath(node.path)
  const text = selectAll(scope.schema, node.name)
  const tab = newScript(connId, scope, text, node.name)
  void run(tab, { mode: RunMode.RANGE, start: 0, end: text.length })
}

// insertText replaces the active editor's selection with text and selects it.
export function insertText(text: string, select = false): { tab: Tab; from: number; to: number } | null {
  const tab = tabs.active
  if (!tab) return null
  const sel = tabs.state(tab).selection.main
  const to = sel.from + text.length
  tabs.update(tab, {
    changes: { from: sel.from, to: sel.to, insert: text },
    selection: select ? { anchor: sel.from, head: to } : { anchor: to },
    effects: EditorView.scrollIntoView(sel.from, { y: 'nearest' }),
    userEvent: 'input',
  })
  if (editorHost.shown === tab) editorHost.view?.focus()
  return { tab, from: sel.from, to }
}

// runText inserts text at the cursor and runs it.
export function runText(text: string) {
  const at = insertText(text, true)
  if (at) void run(at.tab, { mode: RunMode.RANGE, start: at.from, end: at.to })
}

export function closeTab(tab: Tab) {
  workspace.closeTab(tab).catch((err: unknown) => notify.error(err, 'Close tab'))
}

// saveTab saves a tab (asking where when it is untitled).
export function saveTab(tab: Tab, as = false) {
  ;(as ? workspace.saveAs(tab) : workspace.save(tab)).catch((err: unknown) => notify.error(err, `Save ${tab.title}`))
}

async function saveConfig(id: string, change: (c: ConnectionConfig) => void, what: string) {
  const cfg = connections.config(id)
  if (!cfg) return
  const next = clone(ConnectionConfigSchema, cfg)
  change(next)
  try {
    await connections.save(next, { forgetPassword: false })
  } catch (err) {
    notify.error(err, what)
  }
}

export function setFavorite(id: string, favorite: boolean) {
  return saveConfig(id, (c) => (c.favorite = favorite), favorite ? 'Add to favorites' : 'Remove from favorites')
}

export function groupNames(): string[] {
  return [...new Set(connections.list.map((c) => c.config!.group).filter(Boolean))].sort()
}

export async function moveToGroup(id: string) {
  const cfg = connections.config(id)
  if (!cfg) return
  const group = await prompts.prompt({
    title: `Group of ${cfg.name}`,
    label: 'Group (empty for none)',
    value: cfg.group,
    placeholder: 'e.g. Billing',
    suggestions: groupNames(),
    confirm: 'Move',
  })
  if (group === null || group.trim() === cfg.group) return
  await saveConfig(id, (c) => (c.group = group.trim()), 'Move to group')
}

export async function renameGroup(name: string) {
  const next = await prompts.prompt({ title: `Rename group ${name}`, label: 'Name (empty ungroups)', value: name, confirm: 'Rename' })
  if (next === null || next.trim() === name) return
  for (const c of connections.list.filter((c) => c.config!.group === name)) {
    await saveConfig(c.config!.id, (x) => (x.group = next.trim()), 'Rename group')
  }
}

export async function run(tab: Tab, target = tabs.target(tab, false)) {
  try {
    await tabs.run(tab, target)
  } catch (err) {
    notify.error(err, 'Run')
  }
}

export function editConnection(id: string) {
  ui.editing = id
}

// deleteConnection asks, deletes and closes the tabs bound to the connection.
export async function deleteConnection(id: string): Promise<boolean> {
  const name = connections.name(id)
  const bound = tabs.tabs.filter((t) => t.connId === id)
  const ok = await prompts.ask({
    title: 'Delete connection',
    text:
      `Delete "${name}"? Its saved password is removed too.` +
      (bound.length ? `\n${bound.length} open ${bound.length === 1 ? 'tab is' : 'tabs are'} closed.` : ''),
    confirm: 'Delete',
    danger: true,
  })
  if (!ok) return false
  try {
    await connections.remove(id)
  } catch (err) {
    notify.error(err, `Delete ${name}`)
    return false
  }
  explorer.forget(id)
  for (const t of bound) await workspace.closeTab(t).catch(() => {})
  notify.flash(`Deleted ${name}`)
  return true
}

// guard runs a background action, reporting its failure as a toast.
export function guard(what: string, f: () => Promise<unknown>) {
  f().catch((err: unknown) => notify.error(err, what))
}
