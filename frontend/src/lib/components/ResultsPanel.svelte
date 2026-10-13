<script lang="ts">
  import { guard } from '../app/actions'
  import { MESSAGES, tabs, type Tab } from '../app/tabs.svelte'
  import Grid from '../grid/Grid.svelte'

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

  const view = $derived(tab.view === MESSAGES || tab.view >= snap.results.length ? MESSAGES : tab.view)
  const current = $derived(view === MESSAGES ? null : snap.results[view])

  let panel = $state<HTMLElement>()

  export function focus() {
    ;(panel?.querySelector('.grid') as HTMLElement | null)?.focus() ?? panel?.focus()
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    const ids = [...snap.results.keys(), MESSAGES]
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
  </div>

  <div class="body">
    {#if current}
      <div class="grid-wrap">
        <Grid buffer={current.r.buffer} version={tab.version} />
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
    background: var(--base);
    outline: none;
  }

  .subtabs {
    display: flex;
    gap: 2px;
    height: 30px;
    padding: 0 6px;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    overflow-x: auto;
    scrollbar-width: none;
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

  .count {
    color: var(--overlay0);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
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
    height: 30px;
    padding: 0 10px;
    background: var(--mantle);
    border-top: 1px solid var(--surface0);
    color: var(--subtext0);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  footer b {
    color: var(--text);
  }

  .muted {
    color: var(--overlay0);
  }

  .warn {
    color: var(--peach);
  }

  .more {
    margin-left: auto;
    color: var(--yellow);
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
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
    color: var(--overlay1);
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
    color: var(--overlay0);
    font-family: var(--font-ui);
    font-size: 13px;
  }
</style>
