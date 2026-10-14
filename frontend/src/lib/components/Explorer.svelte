<script lang="ts">
  import {
    connectAndExpand,
    deleteConnection,
    disconnect,
    editConnection,
    exportNode,
    guard,
    moveToGroup,
    newScript,
    openObject,
    renameGroup,
    openTerminal,
    canImport,
    importCsv,
    selectFrom,
    setFavorite,
  } from '../app/actions'
  import { connections, ConnectionState, stateLabel } from '../app/connections.svelte'
  import { explorer, type Row } from '../app/explorer.svelte'
  import { menu, type MenuEntry } from '../app/menu.svelte'
  import { tabs } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { workspace } from '../app/workspace.svelte'
  import { dragName } from '../explorer/tree'
  import type { FileInfo } from '../gen/quarry/v1/workspace_pb'
  import { scopeOfPath } from '../sql'
  import DriverBadge from '../ui/DriverBadge.svelte'
  import Icon from '../ui/Icon.svelte'

  let tree = $state<HTMLDivElement>()

  const OPENABLE = new Set(['table', 'view', 'collection', 'key'])
  const SQL_OBJECT = new Set(['table', 'view'])
  const SCOPED = new Set(['database', 'schema'])

  const rows = $derived(explorer.rows)
  const activeIndex = $derived(rows.findIndex((r) => r.key === explorer.selected))

  function expandable(r: Row): boolean {
    return r.type !== 'node' || !r.node.leaf
  }

  async function toggle(r: Row) {
    if (r.type === 'group') return explorer.toggleGroup(r.path)
    if (r.expanded) return explorer.collapse(r.key)
    if (r.type === 'conn') return connectAndExpand(r.conn.id)
    if (!r.node.leaf) await explorer.expand(r.connId, r.node)
  }

  function activate(r: Row) {
    explorer.selected = r.key
    if (r.type === 'node' && OPENABLE.has(r.node.kind)) openObject(r.connId, r.node)
    else if (r.type === 'conn' && !r.expanded) connectAndExpand(r.conn.id)
    else void toggle(r)
  }

  function refresh(r: Row) {
    if (r.type === 'conn') void explorer.refresh(r.conn.id, null)
    else if (r.type === 'node') void explorer.refresh(r.connId, r.node)
  }

  function items(r: Row): MenuEntry[] {
    if (r.type === 'group') {
      return [
        { label: r.expanded ? 'Collapse' : 'Expand', run: () => explorer.toggleGroup(r.path) },
        'separator',
        { label: 'Rename folder…', icon: 'edit', run: () => void renameGroup(r.path) },
      ]
    }
    if (r.type === 'conn') {
      const id = r.conn.id
      const state = connections.state(id)
      const live = state === ConnectionState.CONNECTED || state === ConnectionState.LOST
      return [
        { label: 'New script', icon: 'script', run: () => newScript(id) },
        { label: 'New scratch', icon: 'scratch', run: () => guard('New scratch', () => workspace.newScratch(id)) },
        { label: 'Open terminal', icon: 'terminal', run: () => void openTerminal(id) },
        'separator',
        live
          ? { label: 'Disconnect', icon: 'unplug', run: () => disconnect(id) }
          : { label: 'Connect', icon: 'connection', run: () => connectAndExpand(id) },
        { label: 'Refresh', icon: 'refresh', hint: 'F5', disabled: !live, run: () => refresh(r) },
        'separator',
        r.conn.favorite
          ? { label: 'Remove from favorites', icon: 'star', run: () => void setFavorite(id, false) }
          : { label: 'Add to favorites', icon: 'star', run: () => void setFavorite(id, true) },
        { label: 'Move to folder…', icon: 'folder', run: () => void moveToGroup(id) },
        'separator',
        { label: 'Edit…', icon: 'edit', run: () => editConnection(id) },
        { label: 'Delete…', icon: 'trash', danger: true, run: () => deleteConnection(id) },
      ]
    }
    const n = r.node
    const out: MenuEntry[] = []
    if (OPENABLE.has(n.kind)) {
      out.push(
        { label: `Open ${n.kind}`, icon: n.kind === 'view' ? 'view' : n.kind === 'key' ? 'key' : 'table', hint: 'Enter', run: () => openObject(r.connId, n) },
        ...(SQL_OBJECT.has(n.kind) && n.path.length > 2 ? [{ label: 'New script with SELECT', icon: 'play', run: () => selectFrom(r.connId, n) }] : []),
        { label: 'Export data…', icon: 'export', run: () => exportNode(r.connId, n) },
        ...(canImport(r.connId, n.kind) ? [{ label: 'Import CSV…', icon: 'open', run: () => importCsv(r.connId, n) }] : []),
        'separator',
      )
    }
    if (n.path.length >= 1) {
      const scope = scopeOfPath(n.path, connections.config(r.connId)?.kind)
      out.push({
        label: SCOPED.has(n.kind) ? `New script in ${n.name}` : 'New script here',
        icon: 'script',
        run: () => newScript(r.connId, scope),
      })
      if (SCOPED.has(n.kind)) out.push({ label: `Open terminal in ${n.name}`, icon: 'terminal', run: () => void openTerminal(r.connId, scope) })
    }
    if (!n.leaf) {
      if (out.length) out.push('separator')
      out.push({ label: 'Refresh', icon: 'refresh', hint: 'F5', run: () => refresh(r) })
    }
    return out
  }

  function oncontextmenu(e: MouseEvent, r: Row) {
    explorer.selected = r.key
    const list = items(r)
    if (list.length) menu.open(e, list)
  }

  function ondragstart(e: DragEvent, r: Row) {
    const name = r.type === 'node' ? dragName(r.node, connections.config(r.connId)?.kind) : ''
    if (!name || !e.dataTransfer) {
      e.preventDefault()
      return
    }
    e.dataTransfer.setData('text/plain', name)
    e.dataTransfer.effectAllowed = 'copy'
  }

  // --- scratches ---

  $effect(() => {
    void workspace.loadScratches()
  })

  function openScratch(f: FileInfo) {
    guard(`Open ${f.name}`, () => workspace.openPath(f.path))
  }

  function scratchMenu(e: MouseEvent, f: FileInfo) {
    menu.open(e, [
      { label: 'Open', icon: 'open', run: () => openScratch(f) },
      'separator',
      { label: 'Delete…', icon: 'trash', danger: true, run: () => void workspace.deleteScratch(f) },
    ])
  }

  function ago(f: FileInfo): string {
    if (!f.modified) return ''
    const s = Math.max(0, Date.now() / 1000 - Number(f.modified.seconds))
    if (s < 60) return 'now'
    if (s < 3600) return `${Math.floor(s / 60)} min`
    if (s < 86400) return `${Math.floor(s / 3600)} h`
    return `${Math.floor(s / 86400)} d`
  }

  function parentOf(i: number): number {
    const d = rows[i].depth
    for (let j = i - 1; j >= 0; j--) if (rows[j].depth < d) return j
    return -1
  }

  function select(i: number) {
    const r = rows[i]
    if (!r) return
    explorer.selected = r.key
    tree?.querySelector(`[data-i="${i}"]`)?.scrollIntoView({ block: 'nearest' })
  }

  function onkeydown(e: KeyboardEvent) {
    const i = activeIndex
    const r = rows[i]
    switch (e.key) {
      case 'ArrowDown':
        select(Math.min(rows.length - 1, i + 1))
        break
      case 'ArrowUp':
        select(i < 0 ? 0 : Math.max(0, i - 1))
        break
      case 'Home':
        select(0)
        break
      case 'End':
        select(rows.length - 1)
        break
      case 'ArrowRight':
        if (!r || !expandable(r)) return
        if (r.expanded) select(i + 1)
        else void toggle(r)
        break
      case 'ArrowLeft':
        if (!r) return
        if (r.expanded && expandable(r)) void toggle(r)
        else select(parentOf(i))
        break
      case 'Enter':
        if (r) activate(r)
        break
      case 'F5':
        if (r) refresh(r)
        break
      default:
        return
    }
    e.preventDefault()
  }

  function statusClass(id: string): string {
    return stateLabel(connections.state(id))
  }

  export function focus() {
    tree?.focus()
    if (activeIndex < 0 && rows.length) explorer.selected = rows[0].key
  }

  const NODE_ICONS: Record<string, string> = {
    database: 'database',
    schema: 'schema',
    folder: 'folder',
    table: 'table',
    view: 'view',
    column: 'column',
    index: 'index',
    function: 'function',
    sequence: 'sequence',
    key: 'key',
    collection: 'table',
  }
</script>

<aside class="explorer">
  <header>
    <span class="title">Connections</span>
    <span class="actions">
      <button class="icon-btn" type="button" title="Import connections" onclick={() => (ui.importingConnections = true)}>
        <Icon name="download" size={14} />
      </button>
      <button class="icon-btn" type="button" title="New connection (Ctrl+Shift+N)" onclick={() => (ui.editing = '')}>
        <Icon name="plus" size={15} />
      </button>
    </span>
  </header>
  <div class="filter">
    <Icon name="search" size={13} />
    <input
      type="text"
      placeholder="Filter"
      bind:value={explorer.filter}
      onkeydown={(e) => {
        if (e.key === 'Escape' && explorer.filter) {
          explorer.filter = ''
          e.stopPropagation()
        } else if (e.key === 'ArrowDown') {
          e.preventDefault()
          focus()
        }
      }}
    />
  </div>

  <div class="tree" role="tree" tabindex="0" bind:this={tree} {onkeydown} aria-label="Connections">
    {#each rows as r, i (r.key)}
      <div
        class="row"
        class:selected={r.key === explorer.selected}
        class:group={r.type === 'group'}
        role="treeitem"
        aria-selected={r.key === explorer.selected}
        aria-expanded={expandable(r) ? r.expanded : undefined}
        tabindex="-1"
        data-i={i}
        style:padding-left="{6 + r.depth * 16}px"
        draggable={r.type === 'node' && !!dragName(r.node, connections.config(r.connId)?.kind)}
        ondragstart={(e) => ondragstart(e, r)}
        onclick={() => (explorer.selected = r.key)}
        ondblclick={() => activate(r)}
        oncontextmenu={(e) => oncontextmenu(e, r)}
        onkeydown={() => {}}
      >
        {#if expandable(r)}
          <button
            class="twisty"
            class:open={r.expanded}
            type="button"
            tabindex="-1"
            aria-label={r.expanded ? 'Collapse' : 'Expand'}
            onclick={(e) => {
              e.stopPropagation()
              explorer.selected = r.key
              void toggle(r)
            }}
            ondblclick={(e) => e.stopPropagation()}><Icon name="chevron" size={11} /></button
          >
        {:else}
          <span class="twisty"></span>
        {/if}

        {#if r.type === 'group'}
          <span class="kind folder"><Icon name={r.expanded ? 'folderOpen' : 'folder'} size={14} /></span>
          <span class="name">{r.name}</span>
          <span class="end count">{r.count}</span>
        {:else if r.type === 'conn'}
          {@const st = connections.status(r.conn.id)}
          <DriverBadge kind={r.conn.kind} active={st?.state === ConnectionState.CONNECTED} />
          <span class="name">{r.conn.name}</span>
          {#if r.conn.env === 'prod'}<span class="env-tag" data-env="prod">prod</span>{/if}
          {#if r.conn.favorite}<span class="star" title="Favorite"><Icon name="star" size={11} fill /></span>{/if}
          <span class="end">
            {#if r.loading || st?.state === ConnectionState.CONNECTING}
              <span class="spinner"></span>
            {:else if st?.state === ConnectionState.CONNECTED}
              <span class="latency" title="Connected, last ping {st.latencyMs} ms">{st.latencyMs > 0n ? `${st.latencyMs} ms` : ''}</span>
            {:else if st?.state === ConnectionState.LOST || st?.state === ConnectionState.FAILED}
              <span class="status {statusClass(r.conn.id)}" title={st.error || statusClass(r.conn.id)}>
                <Icon name="alert" size={12} />
              </span>
            {/if}
          </span>
        {:else}
          <span class="kind {r.node.kind}"><Icon name={NODE_ICONS[r.node.kind] ?? 'folder'} size={13} /></span>
          <span class="name">{r.node.name}</span>
          {#if r.node.detail}<span class="detail">{r.node.detail}</span>{/if}
          {#if r.loading}<span class="end"><span class="spinner"></span></span>{/if}
        {/if}
      </div>
      {#if r.type !== 'group' && r.error && !r.loading}
        <div class="row-error" style:padding-left="{30 + r.depth * 16}px" title={r.error}>
          <span>{r.error}</span>
          <button
            class="retry"
            type="button"
            onclick={() =>
              r.type === 'conn' ? connectAndExpand(r.conn.id) : explorer.expand(r.connId, r.node)}>Retry</button
          >
        </div>
      {/if}
    {:else}
      <div class="empty">
        {#if !connections.loaded}
          <span class="spinner"></span>
        {:else if connections.loadError}
          <p class="err">{connections.loadError}</p>
          <button class="btn" type="button" onclick={() => connections.load().catch(() => {})}>Retry</button>
        {:else if explorer.filter}
          <p>Nothing loaded matches “{explorer.filter}”.</p>
        {:else}
          <p>No connections yet.</p>
          <button class="btn primary" type="button" onclick={() => (ui.editing = '')}>New connection</button>
          <button class="btn" type="button" onclick={() => (ui.importingConnections = true)}>Import from pgpass, tnsnames…</button>
        {/if}
      </div>
    {/each}
  </div>

  <section class="scratches">
    <header>
      <button class="section" type="button" onclick={() => (ui.layout.scratches = !ui.layout.scratches)}>
        <span class="twisty" class:open={ui.layout.scratches}><Icon name="chevron" size={11} /></span>
        <span class="title">Scratches</span>
        <span class="n">{workspace.scratches.length || ''}</span>
      </button>
      <button class="icon-btn" type="button" title="New scratch" onclick={() => guard('New scratch', () => workspace.newScratch())}>
        <Icon name="plus" />
      </button>
    </header>
    {#if ui.layout.scratches}
      <div class="slist">
        {#each workspace.scratches as f (f.path)}
          {@const open = tabs.byPath(f.path)}
          <div
            class="srow"
            class:current={!!open && open.key === tabs.activeKey}
            role="button"
            tabindex="0"
            title={f.path}
            onclick={() => openScratch(f)}
            onkeydown={(e) => {
              if (e.key === 'Enter') openScratch(f)
              else if (e.key === 'Delete') void workspace.deleteScratch(f)
            }}
            oncontextmenu={(e) => scratchMenu(e, f)}
          >
            <span class="kind"><Icon name="scratch" size={13} /></span>
            <span class="name">{f.name}</span>
            {#if open?.dirty}<span class="dirty">●</span>{/if}
            <span class="detail">{ago(f)}</span>
            <button
              class="del"
              type="button"
              tabindex="-1"
              title="Delete"
              onclick={(e) => {
                e.stopPropagation()
                void workspace.deleteScratch(f)
              }}><Icon name="trash" size={12} /></button
            >
          </div>
        {:else}
          <p class="none">No scratches yet.</p>
        {/each}
      </div>
    {/if}
  </section>
</aside>

<style>
  .explorer {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg-side);
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 36px;
    padding: 0 6px 0 14px;
  }

  .title {
    color: var(--text-2);
    font-size: 12px;
    font-weight: 600;
  }

  .actions {
    display: flex;
    gap: 2px;
  }

  .filter {
    display: flex;
    align-items: center;
    gap: 7px;
    margin: 0 10px 8px;
    padding: 0 9px;
    height: 28px;
    background: var(--bg-hover);
    border: 1px solid transparent;
    border-radius: var(--radius);
    color: var(--text-3);
    transition:
      border-color 0.12s,
      box-shadow 0.12s;
  }

  .filter:focus-within {
    border-color: color-mix(in srgb, var(--accent) 60%, transparent);
    box-shadow: var(--ring);
  }

  .filter input {
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    outline: none;
    color: var(--text-1);
  }

  .filter input::placeholder {
    color: var(--text-3);
  }

  .tree {
    flex: 1;
    overflow: auto;
    padding: 0 6px 8px;
    outline: none;
  }

  .row {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 26px;
    padding-right: 8px;
    border-radius: var(--radius);
    color: var(--text-1);
    cursor: default;
    white-space: nowrap;
    user-select: none;
  }

  .row:hover {
    background: var(--bg-hover);
  }

  .row.selected {
    background: var(--bg-active);
  }

  .tree:focus-visible .row.selected {
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 60%, transparent);
  }

  .row.group .name {
    color: var(--text-1);
  }

  .twisty {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: 14px;
    height: 14px;
    margin-right: -3px;
    padding: 0;
    background: none;
    border: none;
    border-radius: var(--radius-s);
    color: var(--text-3);
    cursor: pointer;
    transition: transform 0.12s;
  }

  .twisty.open {
    transform: rotate(90deg);
  }

  .name {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .star {
    display: inline-flex;
    color: var(--yellow);
  }

  .detail {
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-3);
    font-size: 11px;
  }

  .end {
    display: inline-flex;
    align-items: center;
    margin-left: auto;
    padding-left: 6px;
  }

  .count,
  .latency {
    color: var(--text-3);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }

  .status.lost {
    color: var(--peach);
  }

  .status.failed {
    color: var(--red);
  }

  .kind {
    display: inline-flex;
    flex: none;
    color: var(--text-3);
  }

  .kind.folder {
    color: color-mix(in srgb, var(--yellow) 70%, var(--overlay1));
  }

  .kind.database {
    color: var(--peach);
  }

  .kind.schema {
    color: var(--mauve);
  }

  .kind.table,
  .kind.collection {
    color: var(--blue);
  }

  .kind.view {
    color: var(--teal);
  }

  .kind.function {
    color: var(--pink);
  }

  .kind.index,
  .kind.key {
    color: var(--yellow);
  }

  .row-error {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
    padding-right: 8px;
    color: var(--red);
    font-size: 12px;
  }

  .row-error span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .retry {
    flex: none;
    padding: 0;
    background: none;
    border: none;
    color: var(--blue);
    cursor: pointer;
  }

  .retry:hover {
    text-decoration: underline;
  }

  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 32px 16px;
    color: var(--text-2);
    text-align: center;
  }

  .empty p {
    margin: 0 0 4px;
  }

  .empty .err {
    color: var(--red);
  }

  .scratches {
    flex: none;
    max-height: 40%;
    display: flex;
    flex-direction: column;
    border-top: 1px solid var(--line);
  }

  .scratches header {
    padding-left: 8px;
  }

  .section {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 26px;
    padding: 0 6px;
    background: none;
    border: none;
    border-radius: var(--radius);
    color: var(--text-2);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }

  .section:hover {
    color: var(--text-1);
  }

  .n {
    color: var(--text-3);
    font-weight: 400;
  }

  .slist {
    overflow: auto;
    padding: 0 6px 8px;
  }

  .srow {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 26px;
    padding: 0 6px 0 10px;
    border-radius: var(--radius);
    cursor: default;
    outline: none;
  }

  .srow:hover,
  .srow:focus-visible {
    background: var(--bg-hover);
  }

  .srow.current .name {
    color: var(--accent);
  }

  .dirty {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-2);
    font-size: 0;
  }

  .srow .detail {
    margin-left: auto;
  }

  .del {
    display: inline-flex;
    padding: 2px;
    background: none;
    border: none;
    border-radius: var(--radius-s);
    color: var(--text-3);
    cursor: pointer;
    opacity: 0;
  }

  .srow:hover .del {
    opacity: 1;
  }

  .del:hover {
    color: var(--red);
  }

  .none {
    margin: 0;
    padding: 4px 12px 8px;
    color: var(--text-3);
    font-size: 12px;
  }
</style>
