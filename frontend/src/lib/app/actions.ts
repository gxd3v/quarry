// User actions that span several stores, shared by the explorer, menus and
// the keymap.
import { clone } from '@bufbuild/protobuf'
import { EditorView } from '@codemirror/view'
import { ConnectionConfigSchema, type ConnectionConfig } from '../gen/quarry/v1/connection_pb'
import { RunMode } from '../gen/quarry/v1/query_pb'
import type { ResultSet } from '../run/model'
import { schemaOfObject, scopeOfPath, selectAll, type ScopeLike } from '../sql'
import { MAX_TERMINALS, TerminalSession } from '../terminal/session.svelte'
import { orderBy } from '../table/order'
import { connections } from './connections.svelte'
import { explorer, type TreeNode } from './explorer.svelte'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import { loadData, openTable } from './table.svelte'
import { Listener, parseChannels } from './listen.svelte'
import { editorHost, NOTIFICATIONS, tabs, type Tab } from './tabs.svelte'
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

// openTerminal opens a terminal tab running the connection's native CLI.
export function openTerminal(connId: string, scope?: ScopeLike): Tab | null {
  if (tabs.tabs.filter((t) => t.kind === 'terminal').length >= MAX_TERMINALS) {
    notify.toast('error', `At most ${MAX_TERMINALS} terminals can be open; close one first`)
    return null
  }
  const cfg = connections.config(connId)
  const where = scope ?? { database: cfg?.database ?? '', schema: '' }
  const term = new TerminalSession(connId, where, () => prompts.askPassword(`Password for ${connections.name(connId)}`, ''))
  const tab = tabs.open(connId, where, '', { title: 'Shell', terminal: term })
  void term.start()
  return tab
}

// pickAndOpenTerminal opens a terminal on the active tab's connection, or
// asks for one.
export async function pickAndOpenTerminal() {
  const t = tabs.active
  if (t && t.kind !== 'terminal') return void openTerminal(t.connId, t.scope)
  const id = await ui.pickConnection('Open terminal on…')
  if (id) openTerminal(id)
}

// listen asks for channels and listens to them in the tab's results panel.
export async function listen(tab: Tab) {
  const prev = tab.listener
  const v = await prompts.prompt({
    title: `Listen on ${connections.name(tab.connId)}`,
    label: 'Channels, separated by commas',
    value: prev?.channels.join(', ') ?? '',
    placeholder: 'e.g. jobs, events',
    confirm: 'Listen',
  })
  const channels = parseChannels(v ?? '')
  if (!channels.length) return
  prev?.stop()
  const l = new Listener(tab.connId, { ...tab.scope }, channels)
  tab.listener = l
  tab.view = NOTIFICATIONS
  void l.start()
}

// openObject opens the table tab of a table or view.
export function openObject(connId: string, node: Pick<TreeNode, 'path' | 'name' | 'kind'>) {
  openTable(connId, { path: node.path, kind: node.kind, name: node.name })
}

// selectFrom opens a new script that selects from a table or view, and runs it.
export function selectFrom(connId: string, node: Pick<TreeNode, 'path' | 'name'>) {
  const kind = connections.config(connId)?.kind
  const scope = scopeOfPath(node.path, kind)
  const text = selectAll(schemaOfObject(node.path, kind), node.name)
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

// run runs a script tab's statement, or reloads a table tab.
export async function run(tab: Tab, target?: ReturnType<typeof tabs.target>) {
  try {
    if (tab.kind === 'table') await loadData(tab)
    else await tabs.run(tab, target ?? tabs.target(tab, false))
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

function qualified(connId: string, path: readonly string[], name: string): string {
  const schema = schemaOfObject(path, connections.config(connId)?.kind)
  return schema ? `${schema}.${name}` : name
}

// exportNode opens the export dialog for all rows of a table or view.
export function exportNode(connId: string, node: Pick<TreeNode, 'path' | 'name' | 'kind'>) {
  ui.exporting = {
    connId,
    scope: scopeOfPath(node.path, connections.config(connId)?.kind),
    source: { data: { node: { path: node.path, kind: node.kind, name: node.name }, filter: '', orderBy: [] } },
    name: qualified(connId, node.path, node.name),
  }
}

// canImport reports whether rows can be imported into a node of the
// connection (PostgreSQL tables on writable connections).
export function canImport(connId: string, kind: string): boolean {
  const cfg = connections.config(connId)
  return cfg?.kind === 'postgres' && !cfg.readOnly && kind === 'table'
}

export function importCsv(connId: string, node: Pick<TreeNode, 'path' | 'name' | 'kind'>) {
  ui.importingCsv = { connId, node: { path: [...node.path], kind: node.kind, name: node.name } }
}

// exportTableData exports a table tab's rows with its filter and order.
export function exportTableData(tab: Tab) {
  const t = tab.table
  if (!t) return
  ui.exporting = {
    connId: tab.connId,
    scope: { ...tab.scope },
    source: { data: { node: t.node, filter: t.filter, orderBy: orderBy(t.order) } },
    name: qualified(tab.connId, t.node.path, t.node.name),
  }
}

// exportResult exports a result by running its statement again on the server.
export function exportResult(tab: Tab, r: ResultSet) {
  if (!r.sql.trim()) {
    notify.toast('error', 'The statement of this result is unknown; run it again to export it')
    return
  }
  ui.exporting = { connId: tab.connId, scope: { ...tab.scope }, source: { sql: r.sql }, name: `${tab.title} result ${r.statement + 1}` }
}

// guard runs a background action, reporting its failure as a toast.
export function guard(what: string, f: () => Promise<unknown>) {
  f().catch((err: unknown) => notify.error(err, what))
}
