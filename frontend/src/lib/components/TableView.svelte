<script lang="ts">
  import { exportTableData, guard } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { prompts } from '../app/prompts.svelte'
  import { schemaOfObject } from '../sql'
  import { flavorOf, keyTTL, loadDescribe, redisKey, setTTL, viewsFor, type TableView } from '../app/table.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import TableData from './TableData.svelte'
  import TableDetails from './TableDetails.svelte'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  const t = $derived(tab.table!)
  const flavor = $derived(flavorOf(connections.config(tab.connId)?.kind ?? ''))
  const views = $derived(viewsFor(flavor))
  const isKey = $derived(flavor === 'redis' && t.node.kind === 'key')
  const ttl = $derived(isKey ? keyTTL(t) : '')

  // Redis keys show their TTL, which comes with the stats.
  $effect(() => {
    if (isKey && !t.describe && !t.describing && !t.describeError) void loadDescribe(tab)
  })

  async function editTTL() {
    const v = await prompts.prompt({
      title: `Expiry of ${t.node.name}`,
      label: 'Seconds until the key expires; empty or 0 removes the expiry',
      placeholder: 'e.g. 3600',
      confirm: 'Set',
    })
    if (v === null) return
    const secs = Number(v.trim() || '0')
    if (!Number.isFinite(secs) || secs < 0) return void prompts.ask({ title: 'Expiry', text: `"${v}" is not a number of seconds.`, confirm: 'OK' })
    guard('Set expiry', () => setTTL(tab, secs))
  }
  let data = $state<ReturnType<typeof TableData>>()

  export function shownGrid() {
    return t.view === 'data' ? (data?.shownGrid() ?? null) : null
  }

  export function scrollPos() {
    return data?.scrollPos() ?? null
  }

  export function focus() {
    data?.focus()
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    const i = views.findIndex((v) => v.view === t.view)
    const n = views.length
    const next = views[(i + (e.key === 'ArrowRight' ? 1 : n - 1)) % n].view
    t.view = next
    e.preventDefault()
    ;(e.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-view="${next}"]`)?.focus()
  }

  const kindLabel = $derived(t.node.kind === 'view' || t.node.kind === 'collection' || t.node.kind === 'key' ? t.node.kind : flavor === 'redis' ? 'keys' : 'table')
  const schema = $derived(schemaOfObject(t.node.path, connections.config(tab.connId)?.kind))
  const qualifier = $derived(schema ? `${schema}.` : '')
  const select = (v: TableView) => (t.view = v)
</script>

<section class="table-view">
  <div class="subtabs" role="tablist" tabindex="-1" {onkeydown}>
    <span class="what" title={t.node.path.join(' › ')}>{kindLabel} <b>{qualifier}{(isKey && redisKey(t.node.path)) || t.node.name}</b></span>
    {#if isKey}
      <button class="ttl" type="button" title="Change the expiry" onclick={editTTL}>TTL {ttl || '…'}</button>
    {/if}
    {#each views as v (v.view)}
      <button
        type="button"
        role="tab"
        data-view={v.view}
        aria-selected={t.view === v.view}
        tabindex={t.view === v.view ? 0 : -1}
        class:active={t.view === v.view}
        onclick={() => select(v.view)}
      >
        {v.label}
        {#if v.view === 'data' && t.changes}<span class="dot" title="Pending changes">●</span>{/if}
      </button>
    {/each}
  </div>
  <div class="body">
    <!-- Data stays mounted so the grid keeps its selection and scroll. -->
    <div class="pane" class:hidden={t.view !== 'data'}>
      <TableData bind:this={data} {tab} onexport={() => exportTableData(tab)} />
    </div>
    {#if t.view !== 'data'}
      <div class="pane"><TableDetails {tab} view={t.view} /></div>
    {/if}
  </div>
</section>

<style>
  .ttl {
    margin-right: 8px;
    height: 22px;
    padding: 0 9px;
    border: none;
    border-radius: 11px;
    background: var(--bg-hover);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 11px;
    cursor: pointer;
  }

  .ttl:hover {
    color: var(--text-1);
    background: var(--bg-active);
  }

  .table-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--bg-main);
  }

  .subtabs {
    display: flex;
    align-items: center;
    gap: 2px;
    height: 38px;
    padding: 0 8px;
    background: var(--bg-main);
    border-bottom: 1px solid var(--line);
    overflow-x: auto;
    scrollbar-width: none;
  }

  .subtabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 26px;
    padding: 0 10px;
    background: none;
    border: none;
    border-radius: var(--radius);
    color: var(--text-3);
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
    transition:
      background 0.12s,
      color 0.12s;
  }

  .subtabs button:hover {
    color: var(--text-1);
    background: var(--bg-hover);
  }

  .subtabs button.active {
    color: var(--text-1);
    background: var(--bg-active);
  }

  .subtabs button:focus-visible {
    outline: none;
    box-shadow: var(--ring);
  }

  .what {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 0 12px 0 6px;
    color: var(--text-3);
    font-size: 12px;
    white-space: nowrap;
  }

  .what b {
    color: var(--text-1);
    font-weight: 600;
  }

  .dot {
    color: var(--peach);
    font-size: 9px;
  }

  .body {
    flex: 1;
    min-height: 0;
    position: relative;
  }

  .pane {
    position: absolute;
    inset: 0;
  }

  .pane.hidden {
    visibility: hidden;
    pointer-events: none;
  }
</style>
