<script lang="ts">
  import { insertText, runText } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import { prompts } from '../app/prompts.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import { workspaceClient } from '../api/clients'
  import { errorMessage } from '../api/errors'
  import type { HistoryEntry } from '../gen/quarry/v1/workspace_pb'
  import Icon from '../ui/Icon.svelte'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  let entries = $state.raw<HistoryEntry[]>([])
  let query = $state('')
  let loading = $state(false)
  let error = $state('')
  let active = $state(-1)
  let list = $state<HTMLDivElement>()
  let seq = 0

  async function load() {
    const connId = tab.connId
    const q = query.trim()
    const mine = ++seq
    loading = true
    try {
      const res = await workspaceClient().history({ connId, query: q, limit: 500 })
      if (mine !== seq) return
      entries = res.entries
      error = ''
      active = Math.min(active, entries.length - 1)
    } catch (err) {
      if (mine === seq) error = errorMessage(err)
    } finally {
      if (mine === seq) loading = false
    }
  }

  // Reload on a new connection, a finished run or (debounced) a new filter.
  const runDone = $derived.by(() => {
    void tab.version
    return tab.run.finished ? tab.run : null
  })
  $effect(() => {
    void tab.connId
    void runDone
    const q = query
    const t = setTimeout(() => void load(), q ? 200 : 0)
    return () => clearTimeout(t)
  })

  let clickTimer: ReturnType<typeof setTimeout> | undefined

  function onclick(i: number) {
    active = i
    clearTimeout(clickTimer)
    clickTimer = setTimeout(() => insertText(entries[i].text), 250)
  }

  function ondblclick(i: number) {
    clearTimeout(clickTimer)
    runText(entries[i].text)
  }

  function onkeydown(e: KeyboardEvent) {
    const n = entries.length
    if (!n) return
    if (e.key === 'ArrowDown') active = Math.min(n - 1, active + 1)
    else if (e.key === 'ArrowUp') active = Math.max(0, active - 1)
    else if (e.key === 'Enter' && active >= 0 && !e.ctrlKey && !e.metaKey) {
      if (e.shiftKey) runText(entries[active].text)
      else insertText(entries[active].text)
    } else return
    e.preventDefault()
    list?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  }

  async function clear() {
    const ok = await prompts.ask({
      title: 'Clear history',
      text: `Clear the query history of ${connections.name(tab.connId)}?`,
      confirm: 'Clear',
      danger: true,
    })
    if (!ok) return
    try {
      await workspaceClient().clearHistory({ connId: tab.connId })
      entries = []
    } catch (err) {
      notify.error(err, 'Clear history')
    }
  }

  const today = new Date().toDateString()
  function when(e: HistoryEntry): string {
    if (!e.at) return ''
    const d = new Date(Number(e.at.seconds) * 1000)
    const p = (n: number) => String(n).padStart(2, '0')
    const time = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
    return d.toDateString() === today ? time : `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${time}`
  }

  function duration(ms: bigint): string {
    const n = Number(ms)
    return n < 1000 ? `${n} ms` : `${(n / 1000).toFixed(n < 10_000 ? 2 : 1)} s`
  }

  function oneLine(s: string): string {
    const t = s.replace(/\s+/g, ' ').trim()
    return t.length > 400 ? t.slice(0, 399) + '…' : t
  }
</script>

<div class="history">
  <div class="bar">
    <div class="filter">
      <Icon name="search" size={13} />
      <input type="text" placeholder="Filter history" bind:value={query} spellcheck="false" />
    </div>
    {#if loading}<span class="spinner"></span>{/if}
    <span class="count">{entries.length.toLocaleString('en-US')} {entries.length === 1 ? 'entry' : 'entries'}</span>
    <button class="btn small" type="button" onclick={() => load()}>Refresh</button>
    <button class="btn small danger" type="button" disabled={!entries.length} onclick={clear}>Clear</button>
  </div>
  {#if error}<p class="error">{error}</p>{/if}
  <div class="list" role="listbox" tabindex="0" bind:this={list} {onkeydown} aria-label="Query history">
    {#each entries as e, i (i)}
      <div
        class="entry"
        class:active={i === active}
        class:failed={!!e.error}
        role="option"
        aria-selected={i === active}
        tabindex="-1"
        data-i={i}
        title={e.error ? `${e.text}\n\nERROR: ${e.error}` : e.text}
        onclick={() => onclick(i)}
        ondblclick={() => ondblclick(i)}
        onkeydown={() => {}}
      >
        <span class="at">{when(e)}</span>
        <span class="ms">{duration(e.durationMs)}</span>
        <span class="rows">
          {#if e.error}<span class="err">error</span>{:else if e.rows >= 0n}{Number(e.rows).toLocaleString('en-US')} {e.rows === 1n ? 'row' : 'rows'}{/if}
        </span>
        <span class="text">{oneLine(e.text)}</span>
      </div>
    {:else}
      <p class="empty">
        {loading ? 'Loading…' : query ? 'Nothing matches.' : 'No queries run on this connection yet.'}
      </p>
    {/each}
  </div>
  <footer>Click inserts into the editor · double-click (or Shift+Enter) runs it</footer>
</div>

<style>
  .history {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-bottom: 1px solid var(--surface0);
  }

  .filter {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
    max-width: 360px;
    height: 24px;
    padding: 0 8px;
    color: var(--overlay0);
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 5px;
  }

  .filter:focus-within {
    border-color: var(--accent);
  }

  .filter input {
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    outline: none;
    color: var(--text);
  }

  .count {
    margin-left: auto;
    color: var(--overlay0);
    font-size: 12px;
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }

  .list {
    flex: 1;
    min-height: 0;
    overflow: auto;
    outline: none;
  }

  .entry {
    display: grid;
    grid-template-columns: auto 64px 90px minmax(0, 1fr);
    gap: 12px;
    align-items: center;
    height: 26px;
    padding: 0 10px;
    font-size: 12px;
    border-bottom: 1px solid color-mix(in srgb, var(--surface0) 50%, transparent);
    cursor: pointer;
  }

  .entry:hover {
    background: color-mix(in srgb, var(--surface0) 55%, transparent);
  }

  .entry.active {
    background: var(--surface0);
  }

  .list:focus-visible .entry.active {
    box-shadow: inset 2px 0 0 var(--accent);
  }

  .at {
    color: var(--overlay1);
    font-variant-numeric: tabular-nums;
  }

  .ms,
  .rows {
    color: var(--subtext0);
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  .err {
    color: var(--red);
  }

  .text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono);
  }

  .failed .text {
    color: var(--maroon);
  }

  .empty,
  .error {
    margin: 8px 12px;
    color: var(--overlay0);
  }

  .error {
    color: var(--red);
  }

  footer {
    padding: 4px 10px;
    color: var(--overlay0);
    font-size: 11px;
    border-top: 1px solid var(--surface0);
    background: var(--mantle);
  }
</style>
