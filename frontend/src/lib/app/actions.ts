// User actions that span several stores, shared by the explorer, menus and
// the keymap.
import { RunMode } from '../gen/quarry/v1/query_pb'
import { scopeOfPath, selectAll, type ScopeLike } from '../sql'
import { connections } from './connections.svelte'
import { explorer, type TreeNode } from './explorer.svelte'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import { tabs, type Tab } from './tabs.svelte'
import { ui } from './ui.svelte'

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

export function newScript(connId: string, scope?: ScopeLike, text = ''): Tab {
  const cfg = connections.config(connId)
  return tabs.open(connId, scope ?? { database: cfg?.database ?? '', schema: '' }, text)
}

// openObject opens a new script that selects from a table or view, and runs it.
export function openObject(connId: string, node: TreeNode) {
  const scope = scopeOfPath(node.path)
  const text = selectAll(scope.schema, node.name)
  const tab = newScript(connId, scope, text)
  void run(tab, { mode: RunMode.RANGE, start: 0, end: text.length })
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
  for (const t of bound) await tabs.close(t).catch(() => {})
  notify.flash(`Deleted ${name}`)
  return true
}

// guard runs a background action, reporting its failure as a toast.
export function guard(what: string, f: () => Promise<unknown>) {
  f().catch((err: unknown) => notify.error(err, what))
}
