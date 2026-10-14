<script lang="ts">
  import { exportResult, guard } from '../app/actions'
  import type { MenuEntry } from '../app/menu.svelte'
  import { connections } from '../app/connections.svelte'
  import { HISTORY, MESSAGES, NOTIFICATIONS, PLAN, tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { guessTable, type CopyOptions } from '../grid/copy'
  import Grid from '../grid/Grid.svelte'
  import { TableSource } from '../table/source'
  import DocumentsView from './DocumentsView.svelte'
  import ExplainView from './ExplainView.svelte'
  import HistoryView from './HistoryView.svelte'
  import NotificationsView from './NotificationsView.svelte'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  // Re-read the plain run model whenever the tab publishes a change.
  const snap = $derived.by(() => {
    void tab.version
    const run = tab.run
    return {
      run,
      results: run.results.map((r) => ({
        r,
        rows: r.buffer.length,
        hasMore: r.hasMore,
        elapsedMs: r.elapsedMs,
        truncated: r.buffer.truncated,
      })),
      messages: run.messages.slice(),
      errors: run.errors,
      finished: run.finished,
      fetchable: run.fetchable,
    }
  })

  const view = $derived(
    tab.view === HISTORY
      ? HISTORY
      : tab.view === PLAN && tab.plan
        ? PLAN
        : tab.view === NOTIFICATIONS && tab.listener
          ? NOTIFICATIONS
          : tab.view < 0 || tab.view >= snap.results.length
            ? MESSAGES
            : tab.view,
  )
  const current = $derived(view < 0 ? null : snap.results[view])
  // MongoDB results end with the whole document; they can show as trees.
  const docCol = $derived(current ? current.r.buffer.columns.findIndex((c) => c.name === '{document}') : -1)
  let asDocs = $state(true)
  const docSource = $derived(current && docCol >= 0 ? new TableSource(current.r.buffer) : null)

  let panel = $state<HTMLElement>()
  let grid = $state<ReturnType<typeof Grid>>()

  export function focus() {
    ;(panel?.querySelector('.grid, .history .list') as HTMLElement | null)?.focus() ?? panel?.focus()
  }

  // The grid of the shown result, if any.
  export function shownGrid(): { grid: ReturnType<typeof Grid>; open: (row: number, col: number) => void } | null {
    return grid && current ? { grid, open: openCell } : null
  }

  function openCell(row: number, col: number) {
    if (current) ui.cell = { buffer: current.r.buffer, row, col }
  }

  function copyOptions(): CopyOptions {
    return { table: guessTable(current?.r.sql ?? ''), dialect: connections.config(tab.connId)?.kind ?? '' }
  }

  // Exports the shown result (its statement runs again on the server).
  export function exportShown(): boolean {
    if (!current) return false
    exportResult(tab, current.r)
    return true
  }

  const menuItems = (): MenuEntry[] => [{ label: 'Export result…', icon: 'export', disabled: !current?.r.sql, run: () => void exportShown() }]

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    const ids = [...snap.results.keys(), MESSAGES, ...(tab.plan ? [PLAN] : []), ...(tab.listener ? [NOTIFICATIONS] : []), HISTORY]
    const i = ids.indexOf(view)
    const next = ids[(i + (e.key === 'ArrowRight' ? 1 : ids.length - 1)) % ids.length]
    tab.view = next
    e.preventDefault()
    ;(e.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-view="${next}"]`)?.focus()
  }

  function count(n: number): string {
    return n.toLocaleString('en-US')
  }
</script>

<section class="results" bind:this={panel} tabindex="-1">
  <div class="subtabs" role="tablist" tabindex="-1" {onkeydown}>
    {#each snap.results as res, i (res.r)}
      <button
        type="button"
        role="tab"
        data-view={i}
        aria-selected={view === i}
        tabindex={view === i ? 0 : -1}
        class:active={view === i}
        onclick={() => (tab.view = i)}
      >
        Result {i + 1}
        <span class="count">{count(res.rows)}{res.hasMore ? '+' : ''}</span>
      </button>
    {/each}
    <button
      type="button"
      role="tab"
      data-view={MESSAGES}
      aria-selected={view === MESSAGES}
      tabindex={view === MESSAGES ? 0 : -1}
      class:active={view === MESSAGES}
      onclick={() => (tab.view = MESSAGES)}
    >
      Messages
      {#if snap.errors}<span class="err-dot" title="{snap.errors} error(s)"></span>{/if}
    </button>
    {#if tab.plan}
      <button
        type="button"
        role="tab"
        data-view={PLAN}
        aria-selected={view === PLAN}
        tabindex={view === PLAN ? 0 : -1}
        class:active={view === PLAN}
        onclick={() => (tab.view = PLAN)}
      >
        Plan
        {#if tab.plan.error}<span class="err-dot" title="Explain failed"></span>{/if}
      </button>
    {/if}
    {#if tab.listener}
      <button
        type="button"
        role="tab"
        data-view={NOTIFICATIONS}
        aria-selected={view === NOTIFICATIONS}
        tabindex={view === NOTIFICATIONS ? 0 : -1}
        class:active={view === NOTIFICATIONS}
        onclick={() => (tab.view = NOTIFICATIONS)}
      >
        Notifications
        <span class="count">{tab.listener.items.length}</span>
        {#if tab.listener.state === 'listening'}<span class="live" title="Listening"></span>{/if}
      </button>
    {/if}
    <button
      type="button"
      role="tab"
      data-view={HISTORY}
      aria-selected={view === HISTORY}
      tabindex={view === HISTORY ? 0 : -1}
      class:active={view === HISTORY}
      onclick={() => (tab.view = HISTORY)}
    >
      History
    </button>
  </div>

  <div class="body">
    {#if view === HISTORY}
      <HistoryView {tab} />
    {:else if view === NOTIFICATIONS && tab.listener}
      <NotificationsView {tab} />
    {:else if view === PLAN && tab.plan}
      {#key tab.plan}<ExplainView plan={tab.plan} />{/key}
    {:else if current}
      <div class="grid-wrap">
        {#if docSource && asDocs}
          <DocumentsView source={docSource} col={docCol} version={tab.version} editable={false} onedit={() => {}} ondelete={() => {}} />
        {:else}
          <Grid bind:this={grid} buffer={current.r.buffer} version={tab.version} oncell={openCell} {copyOptions} {menuItems} />
        {/if}
      </div>
      <footer>
        <span><b>{count(current.rows)}</b> {current.rows === 1 ? 'row' : 'rows'}</span>
        {#if current.truncated}
          <span class="warn">row limit reached</span>
        {/if}
        {#if tab.busy && view === snap.results.length - 1 && !snap.finished}
          <span class="muted">loading…</span>
        {:else if current.elapsedMs}
          <span class="muted">{count(current.elapsedMs)} ms</span>
        {/if}
        {#if docCol >= 0}
          <button class="btn small" type="button" onclick={() => (asDocs = !asDocs)}>{asDocs ? 'Show as grid' : 'Show as documents'}</button>
        {/if}
        <button class="btn small export" type="button" title="Export the whole result to a file" disabled={!current.r.sql} onclick={exportShown}>Export…</button>
        {#if current.hasMore}
          <span class="more">more rows available</span>
          <button class="btn small" type="button" disabled={!!tab.busy || snap.fetchable !== current.r} onclick={() => guard('Load more', () => tabs.fetch(tab, false))}
            >Load more</button
          >
          <button class="btn small" type="button" disabled={!!tab.busy || snap.fetchable !== current.r} onclick={() => guard('Load all', () => tabs.fetch(tab, true))}
            >Load all</button
          >
        {/if}
      </footer>
    {:else}
      <div class="messages">
        {#each snap.messages as m, i (i)}
          <div class="msg {m.kind}">
            {#if m.kind === 'notice'}<span class="sev">{m.severity}</span>{/if}
            {#if m.kind === 'error'}<span class="sev">ERROR{m.code ? ` ${m.code}` : ''}</span>{/if}
            <span class="text">{m.text}</span>
            {#if m.detail}<div class="extra">Detail: {m.detail}</div>{/if}
            {#if m.hint}<div class="extra">Hint: {m.hint}</div>{/if}
            {#if m.kind === 'error' && m.position !== undefined && m.position >= 0}
              <button class="link" type="button" onclick={() => tabs.markError(tab, m.position!)}>Go to position {m.position}</button>
            {/if}
          </div>
        {:else}
          <p class="empty">
            {tab.busy ? 'Running…' : 'Run a statement with Ctrl+Enter, or the whole script with Ctrl+Shift+Enter.'}
          </p>
        {/each}
        {#if snap.finished && snap.messages.length}
          <div class="msg total">Finished in {count(snap.run.elapsedMs)} ms</div>
        {/if}
      </div>
    {/if}
  </div>
</section>

<style>
  .results {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--bg-main);
    outline: none;
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

  .count {
    padding: 0 6px;
    border-radius: 8px;
    background: var(--bg-hover);
    color: var(--text-3);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }

  .live {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--green);
  }

  .err-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--red);
  }

  .body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .grid-wrap {
    flex: 1;
    min-height: 0;
  }

  footer {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 34px;
    padding: 0 10px 0 14px;
    background: var(--bg-main);
    border-top: 1px solid var(--line);
    color: var(--text-2);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  footer b {
    color: var(--text-1);
    font-weight: 600;
  }

  .muted {
    color: var(--text-3);
  }

  .warn {
    color: var(--peach);
  }

  .more {
    color: var(--yellow);
  }

  .export {
    margin-left: auto;
    order: 1;
  }

  .messages {
    flex: 1;
    overflow: auto;
    padding: 8px 12px;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
  }

  .msg {
    padding: 2px 0;
    word-break: break-word;
  }

  .text,
  .extra {
    white-space: pre-wrap;
  }

  .msg.summary {
    color: var(--subtext1);
  }

  .msg.info,
  .msg.total {
    color: var(--text-3);
  }

  .msg.notice .sev {
    color: var(--sky);
  }

  .msg.error {
    color: var(--red);
    padding: 4px 8px;
    margin: 4px 0;
    border-left: 2px solid var(--red);
    background: color-mix(in srgb, var(--red) 8%, transparent);
  }

  .sev {
    margin-right: 8px;
    font-weight: 700;
  }

  .extra {
    color: var(--maroon);
  }

  .link {
    padding: 0;
    background: none;
    border: none;
    color: var(--blue);
    text-decoration: underline;
    cursor: pointer;
    font: inherit;
  }

  .empty {
    margin: 0;
    color: var(--text-3);
    font-family: var(--font-ui);
    font-size: 13px;
  }
</style>
