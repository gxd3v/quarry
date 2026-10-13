<script lang="ts">
  import { ConnectError } from '@connectrpc/connect'
  import { onMount } from 'svelte'
  import { devClient } from '../api/clients'
  import { runBench } from './bench'
  import FpsMeter from './FpsMeter.svelte'
  import { sqlScript } from './script'
  import Editor from '../editor/Editor.svelte'
  import { ResultBuffer } from '../grid/buffer'
  import Grid from '../grid/Grid.svelte'

  const SAMPLE = `-- Quarry A0 benchmark: press "Load 5k-line script" to stress the editor.
SELECT c.id, c.name, count(o.id) AS orders, sum(o.total) AS "Total Spent"
FROM app.customer c
LEFT JOIN app.orders o ON o.customer_id = c.id AND o.created_at >= $1::date
WHERE c.active AND c.city = 'Lisboa'
GROUP BY 1, 2
ORDER BY 4 DESC NULLS LAST
LIMIT 50;
`

  let rowCount = $state(100_000)
  let columnCount = $state(12)
  let delayMs = $state(0)

  let script = $state(SAMPLE)
  let editorKey = $state(0)

  let buffer = $state.raw<ResultBuffer | null>(null)
  let version = $state(0)
  let running = $state(false)
  let error = $state('')
  let stats = $state({ firstMs: null as number | null, totalMs: 0, rows: 0 })
  const rate = $derived(stats.totalMs > 0 ? Math.round((stats.rows * 1000) / stats.totalMs) : 0)

  let controller: AbortController | null = null
  let t0 = 0
  let firstMs: number | null = null
  let endedAt = 0
  let frame = 0

  // Batches can arrive many times per frame; publish them at most once per frame.
  function publish() {
    frame = 0
    version++
    stats = { firstMs, totalMs: (endedAt || performance.now()) - t0, rows: buffer?.length ?? 0 }
  }

  function schedule() {
    frame ||= requestAnimationFrame(publish)
  }

  async function stream() {
    controller?.abort()
    const ctrl = new AbortController()
    controller = ctrl
    error = ''
    running = true
    buffer = null
    t0 = performance.now()
    firstMs = null
    endedAt = 0
    schedule()

    let buf: ResultBuffer | null = null
    try {
      const req = {
        rows: BigInt(Math.max(0, Math.floor(rowCount || 0))),
        columns: Math.max(1, Math.floor(columnCount || 0)),
        delayMs: Math.max(0, Math.floor(delayMs || 0)),
      }
      for await (const msg of devClient().streamRows(req, { signal: ctrl.signal })) {
        if (!buf) {
          firstMs = performance.now() - t0
          buf = new ResultBuffer(msg.columns)
        }
        buf.appendRows(msg.rows)
        if (buffer !== buf) buffer = buf
        schedule()
      }
    } catch (err) {
      if (!ctrl.signal.aborted) error = ConnectError.from(err).message
    } finally {
      if (controller === ctrl) {
        controller = null
        running = false
        endedAt = performance.now()
        schedule()
      }
    }
  }

  function stop() {
    if (!controller) return
    controller.abort()
    controller = null
    running = false
    endedAt = performance.now()
    schedule()
  }

  function loadScript() {
    script = sqlScript(5000)
    editorKey++
  }

  let main: HTMLElement
  let split = $state(0.4)

  function startSplit(e: PointerEvent) {
    const handle = e.currentTarget as HTMLElement
    handle.setPointerCapture(e.pointerId)
    const rect = main.getBoundingClientRect()
    const move = (ev: PointerEvent) => {
      split = Math.min(0.9, Math.max(0.1, (ev.clientY - rect.top) / rect.height))
    }
    const end = () => {
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', end)
      handle.removeEventListener('pointercancel', end)
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', end)
    handle.addEventListener('pointercancel', end)
  }

  // The A0 benchmark drives this screen through the DOM and reports to the backend.
  onMount(() => {
    void (async () => {
      const report = await runBench(100_000).catch((err: unknown) => ({ error: String(err) }))
      await devClient().reportBench({ report: JSON.stringify(report, null, 2) })
    })().catch(() => {})
  })

  function ms(n: number | null): string {
    return n === null ? '–' : `${Math.round(n).toLocaleString()} ms`
  }
</script>

<div class="app">
  <header class="toolbar">
    <label>rows <input type="number" min="0" step="10000" bind:value={rowCount} /></label>
    <label>cols <input type="number" min="1" max="500" bind:value={columnCount} /></label>
    <label>delay ms <input type="number" min="0" bind:value={delayMs} /></label>
    <button class="run" onclick={stream} disabled={running}>▶ Stream</button>
    <button class="stop" onclick={stop} disabled={!running}>■ Stop</button>
    <button onclick={loadScript}>Load 5k-line script</button>

    <span class="stats">
      <span>first <b>{ms(stats.firstMs)}</b></span>
      <span>total <b>{ms(stats.totalMs)}</b></span>
      <span>rows <b>{stats.rows.toLocaleString()}</b></span>
      <span><b>{rate.toLocaleString()}</b> rows/s</span>
      <FpsMeter />
    </span>

    {#if error}
      <span class="error" title={error}>{error}</span>
    {/if}
  </header>

  <main bind:this={main} style:grid-template-rows="{split * 100}% 5px minmax(0, 1fr)">
    <section class="pane">
      {#key editorKey}
        <Editor value={script} />
      {/key}
    </section>
    <div class="divider" role="separator" aria-orientation="horizontal" onpointerdown={startSplit}></div>
    <section class="pane">
      {#if buffer}
        <Grid {buffer} {version} />
      {:else}
        <p class="empty">{running ? 'Waiting for the first batch…' : 'Press ▶ Stream to fetch rows from DevService.'}</p>
      {/if}
    </section>
  </main>
</div>

<style>
  .app {
    height: 100%;
    display: flex;
    flex-direction: column;
  }

  .toolbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 12px;
    padding: 6px 10px;
    background: var(--mantle);
    border-bottom: 1px solid var(--border);
  }

  label {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--subtext0);
  }

  input {
    width: 90px;
    padding: 3px 6px;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 4px;
  }

  input:focus {
    outline: 1px solid var(--accent);
  }

  button {
    padding: 3px 10px;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    cursor: pointer;
  }

  button:hover:not(:disabled) {
    background: var(--surface1);
  }

  button:disabled {
    opacity: 0.45;
    cursor: default;
  }

  .run {
    color: var(--green);
  }

  .stop {
    color: var(--red);
  }

  .stats {
    display: flex;
    gap: 14px;
    margin-left: auto;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }

  .stats b {
    color: var(--text);
    font-weight: 600;
  }

  .error {
    flex-basis: 100%;
    color: var(--red);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  main {
    flex: 1;
    min-height: 0;
    display: grid;
  }

  .pane {
    min-height: 0;
    overflow: hidden;
  }

  .divider {
    background: var(--mantle);
    border-block: 1px solid var(--border);
    cursor: row-resize;
    touch-action: none;
  }

  .divider:hover {
    background: var(--surface1);
  }

  .empty {
    margin: 0;
    padding: 16px;
    color: var(--faint);
  }
</style>
