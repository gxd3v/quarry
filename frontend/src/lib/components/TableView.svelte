<script lang="ts">
  import { exportTableData } from '../app/actions'
  import { TABLE_VIEWS, type TableView } from '../app/table.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import TableData from './TableData.svelte'
  import TableDetails from './TableDetails.svelte'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  const t = $derived(tab.table!)
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
    const i = TABLE_VIEWS.findIndex((v) => v.view === t.view)
    const n = TABLE_VIEWS.length
    const next = TABLE_VIEWS[(i + (e.key === 'ArrowRight' ? 1 : n - 1)) % n].view
    t.view = next
    e.preventDefault()
    ;(e.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-view="${next}"]`)?.focus()
  }

  const kindLabel = $derived(t.node.kind === 'view' ? 'view' : 'table')
  const select = (v: TableView) => (t.view = v)
</script>

<section class="table-view">
  <div class="subtabs" role="tablist" tabindex="-1" {onkeydown}>
    <span class="what" title={t.node.path.join(' › ')}>{kindLabel} <b>{t.node.path[1] ? `${t.node.path[1]}.` : ''}{t.node.name}</b></span>
    {#each TABLE_VIEWS as v (v.view)}
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
  .table-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--base);
  }

  .subtabs {
    display: flex;
    align-items: stretch;
    gap: 2px;
    height: 30px;
    padding: 0 6px;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    overflow-x: auto;
    scrollbar-width: none;
  }

  .what {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 0 10px 0 4px;
    color: var(--overlay1);
    font-size: 12px;
    white-space: nowrap;
  }

  .what b {
    color: var(--blue);
    font-weight: 600;
  }

  .subtabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 10px;
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--overlay1);
    cursor: pointer;
    white-space: nowrap;
  }

  .subtabs button:hover {
    color: var(--subtext1);
  }

  .subtabs button.active {
    color: var(--text);
    border-bottom-color: var(--accent);
  }

  .subtabs button:focus-visible {
    outline: 1px solid var(--accent);
    outline-offset: -2px;
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
