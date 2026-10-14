<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity'
  import { connections } from '../app/connections.svelte'
  import { explorer, nodeKey, type TreeNode } from '../app/explorer.svelte'
  import { notify } from '../app/notify.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import Modal from '../ui/Modal.svelte'
  import PickerList, { type PickItem } from '../ui/PickerList.svelte'

  interface Props {
    tab: Tab
    onclose: () => void
  }

  let { tab, onclose }: Props = $props()

  interface Target {
    connId: string
    database: string
    schema: string
    node: TreeNode | null
  }

  const open = new SvelteSet<string>()

  function currentDb(connId: string): string {
    return tab.connId === connId ? tab.scope.database || connections.config(connId)?.database || '' : ''
  }

  function load(connId: string, node: TreeNode | null) {
    explorer.load(connId, node).catch(() => {})
  }

  // Start with the tab's connection and database expanded.
  $effect.pre(() => {
    const id = tab.connId
    open.add(id)
    load(id, null)
    const db = currentDb(id)
    if (db) {
      open.add(nodeKey(id, [db]))
      load(id, { path: [db], kind: 'database', name: db, detail: '', leaf: false })
    }
  })

  const items: PickItem[] = $derived.by(() => {
    const out: PickItem[] = []
    for (const c of connections.list) {
      const cfg = c.config!
      const id = cfg.id
      const expanded = open.has(id)
      const roots = explorer.children(id)
      out.push({
        key: id,
        data: { connId: id, database: cfg.database, schema: '', node: null } satisfies Target,
        label: cfg.name,
        env: cfg.env || 'local',
        expanded,
        loading: expanded && roots?.loading,
        error: expanded ? roots?.error : '',
        current: tab.connId === id && !tab.scope.database && !tab.scope.schema,
      })
      if (!expanded) continue
      for (const db of roots?.nodes ?? []) {
        const dk = nodeKey(id, db.path)
        const dbOpen = open.has(dk)
        const schemas = explorer.children(dk)
        out.push({
          key: dk,
          data: { connId: id, database: db.name, schema: '', node: db } satisfies Target,
          label: db.name,
          icon: 'database',
          depth: 1,
          detail: db.detail,
          expanded: dbOpen,
          loading: dbOpen && schemas?.loading,
          error: dbOpen ? schemas?.error : '',
          current: tab.connId === id && currentDb(id) === db.name && !tab.scope.schema,
        })
        if (!dbOpen) continue
        for (const s of schemas?.nodes ?? []) {
          if (s.kind !== 'schema') continue
          const sk = nodeKey(id, s.path)
          out.push({
            key: sk,
            data: { connId: id, database: db.name, schema: s.name, node: s } satisfies Target,
            label: s.name,
            icon: 'schema',
            depth: 2,
            detail: s.detail,
            current: tab.connId === id && currentDb(id) === db.name && tab.scope.schema === s.name,
          })
        }
      }
    }
    return out
  })

  function toggle(it: PickItem) {
    const t = it.data as Target
    if (open.has(it.key)) {
      open.delete(it.key)
      return
    }
    open.add(it.key)
    load(t.connId, t.node)
  }

  async function pick(it: PickItem) {
    const t = it.data as Target
    onclose()
    try {
      await tabs.rebind(tab, t.connId, { database: t.database, schema: t.schema })
    } catch (err) {
      notify.error(err, 'Change connection')
    }
  }
</script>

<Modal title="Connection and scope for {tab.title}" width={460} {onclose}>
  <PickerList {items} placeholder="Connection, database or schema" onpick={pick} ontoggle={toggle} />
  <p class="hint">Enter: select · →: expand · A new connection or scope closes the tab's session.</p>
</Modal>

<style>
  .hint {
    margin: 8px 0 0;
    color: var(--text-3);
    font-size: 12px;
  }
</style>
