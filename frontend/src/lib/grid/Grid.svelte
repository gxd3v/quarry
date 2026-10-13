<script lang="ts">
  import type { Cell, ResultBuffer } from './buffer'
  import {
    columnOffsets,
    computeWindow,
    estimateWidths,
    gutterWidth,
    MIN_COL_PX,
    scrollRatio,
    spacerHeight,
  } from './layout'

  interface Props {
    buffer: ResultBuffer
    // Bumped by the owner after appending to the buffer.
    version: number
  }

  let { buffer, version }: Props = $props()

  const ROW_H = 22
  const HEADER_H = 40
  const OVERSCAN_ROWS = 10
  const OVERSCAN_COLS = 2
  const SAMPLE_ROWS = 200

  let scroller: HTMLDivElement
  let clientWidth = $state(0)
  let clientHeight = $state(0)
  let scrollTop = $state(0)
  let scrollLeft = $state(0)

  const rowCount = $derived.by(() => {
    void version
    return buffer.length
  })

  // Writable deriveds: reset when the buffer changes, assigned on resize/click.
  let widths = $derived.by(() => {
    const sample: Cell[][] = []
    for (let i = 0; i < Math.min(buffer.length, SAMPLE_ROWS); i++) sample.push(buffer.row(i))
    return estimateWidths(buffer.columns, sample)
  })
  let sel = $derived.by(() => {
    void buffer
    return { row: -1, col: -1 }
  })

  const offsets = $derived(columnOffsets(widths))
  const totalWidth = $derived(offsets[offsets.length - 1])
  const gutter = $derived(gutterWidth(rowCount))
  const bodyWidth = $derived(Math.max(0, clientWidth - gutter))
  const bodyHeight = $derived(Math.max(0, clientHeight - HEADER_H))
  const spacer = $derived(spacerHeight(rowCount, ROW_H))

  const win = $derived(
    computeWindow({
      scrollTop,
      scrollLeft,
      width: bodyWidth,
      height: bodyHeight,
      rowCount,
      rowHeight: ROW_H,
      offsets,
      overscanRows: OVERSCAN_ROWS,
      overscanCols: OVERSCAN_COLS,
    }),
  )
  // Primitive deriveds stop propagation when a batch arrives but the window is unchanged.
  const rowStart = $derived(win.rowStart)
  const rowEnd = $derived(win.rowEnd)
  const rowTop = $derived(win.rowTop)
  const colStart = $derived(win.colStart)
  const colEnd = $derived(win.colEnd)

  let cache = new Map<number, Cell[]>()
  let cacheOwner: ResultBuffer | undefined

  const rows = $derived.by(() => {
    if (cacheOwner !== buffer) {
      cache = new Map()
      cacheOwner = buffer
    }
    const next = new Map<number, Cell[]>()
    const out: { index: number; cells: Cell[] }[] = []
    for (let r = rowStart; r < rowEnd; r++) {
      const cells = cache.get(r) ?? buffer.row(r)
      next.set(r, cells)
      out.push({ index: r, cells })
    }
    cache = next
    return out
  })

  const cols = $derived(Array.from({ length: colEnd - colStart }, (_, i) => colStart + i))

  const selBox = $derived.by(() => {
    if (sel.row < rowStart || sel.row >= rowEnd || sel.col < 0 || sel.col >= widths.length) return null
    return { x: offsets[sel.col], y: rowTop + (sel.row - rowStart) * ROW_H, w: widths[sel.col] }
  })

  let frame = 0
  function onScroll() {
    if (frame) return
    frame = requestAnimationFrame(() => {
      frame = 0
      scrollTop = scroller.scrollTop
      scrollLeft = scroller.scrollLeft
    })
  }

  $effect(() => {
    void buffer
    scroller.scrollTo(0, 0)
    return () => cancelAnimationFrame(frame)
  })

  function onClick(e: MouseEvent) {
    const cell = (e.target as HTMLElement).closest<HTMLElement>('.cell')
    const row = cell?.parentElement
    if (!cell || !row) return
    sel = { row: Number(row.dataset.r), col: Number(cell.dataset.c) }
  }

  const MOVES: Record<string, [number, number]> = {
    ArrowUp: [-1, 0],
    ArrowDown: [1, 0],
    ArrowLeft: [0, -1],
    ArrowRight: [0, 1],
  }

  function onKeydown(e: KeyboardEvent) {
    const move = MOVES[e.key]
    if (!move || rowCount === 0 || widths.length === 0) return
    e.preventDefault()
    const row = Math.min(Math.max(sel.row + move[0], 0), rowCount - 1)
    const col = Math.min(Math.max(sel.col + move[1], 0), widths.length - 1)
    sel = { row, col }
    reveal(row, col)
  }

  function reveal(row: number, col: number) {
    const ratio = scrollRatio(rowCount, ROW_H, bodyHeight)
    const v = scroller.scrollTop * ratio
    const top = row * ROW_H
    if (top < v) scroller.scrollTop = top / ratio
    else if (top + ROW_H > v + bodyHeight) scroller.scrollTop = (top + ROW_H - bodyHeight) / ratio
    const left = scroller.scrollLeft
    if (offsets[col] < left) scroller.scrollLeft = offsets[col]
    else if (offsets[col + 1] > left + bodyWidth) scroller.scrollLeft = offsets[col + 1] - bodyWidth
  }

  function startResize(e: PointerEvent, col: number) {
    e.preventDefault()
    const handle = e.currentTarget as HTMLElement
    handle.setPointerCapture(e.pointerId)
    const x0 = e.clientX
    const w0 = widths[col]
    const move = (ev: PointerEvent) => {
      const next = widths.slice()
      next[col] = Math.max(MIN_COL_PX, Math.round(w0 + ev.clientX - x0))
      widths = next
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
</script>

<div
  class="grid"
  bind:this={scroller}
  bind:clientWidth
  bind:clientHeight
  onscroll={onScroll}
  onclick={onClick}
  onkeydown={onKeydown}
  role="grid"
  tabindex="0"
  aria-rowcount={rowCount}
  aria-colcount={buffer.columns.length}
>
  <div class="canvas" style:width="{gutter + totalWidth}px" style:height="{HEADER_H + spacer}px">
    <div class="header" style:height="{HEADER_H}px">
      <div class="corner" style:width="{gutter}px"></div>
      {#each cols as c (c)}
        {@const col = buffer.columns[c]}
        <div
          class="hcell"
          class:num={col.numeric}
          style:left="{gutter + offsets[c]}px"
          style:width="{widths[c]}px"
          title="{col.name} {col.type}"
        >
          <span class="name">{col.name}</span><span class="type">{col.type}</span>
          <div
            class="resize"
            role="separator"
            aria-orientation="vertical"
            onpointerdown={(e) => startResize(e, c)}
          ></div>
        </div>
      {/each}
    </div>

    <div class="gutter" style:width="{gutter}px" style:height="{spacer}px">
      <div class="block" style:transform="translateY({rowTop}px)">
        {#each rows as row (row.index)}
          <div class="gcell" class:odd={row.index % 2 === 1}>{row.index + 1}</div>
        {/each}
      </div>
    </div>

    <div
      class="body"
      style:left="{gutter}px"
      style:top="{HEADER_H}px"
      style:width="{totalWidth}px"
      style:height="{spacer}px"
    >
      <div class="block" style:transform="translateY({rowTop}px)">
        {#each rows as row (row.index)}
          <div class="row" class:odd={row.index % 2 === 1} data-r={row.index}>
            {#each cols as c (c)}
              {@const v = row.cells[c]}
              <div
                class="cell"
                class:null={v === null}
                class:num={buffer.columns[c].numeric}
                data-c={c}
                style:left="{offsets[c]}px"
                style:width="{widths[c]}px">{v ?? 'NULL'}</div>
            {/each}
          </div>
        {/each}
      </div>
      {#if selBox}
        <div
          class="selection"
          style:transform="translate({selBox.x}px, {selBox.y}px)"
          style:width="{selBox.w}px"
        ></div>
      {/if}
    </div>
  </div>
</div>

<style>
  .grid {
    position: relative;
    width: 100%;
    height: 100%;
    overflow: auto;
    background: var(--base);
    font-family: var(--font-mono);
    font-size: 12px;
    outline: none;
    contain: strict;
  }

  .canvas {
    position: relative;
  }

  .header {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface1);
  }

  .corner {
    position: sticky;
    left: 0;
    z-index: 1;
    height: 100%;
    background: var(--mantle);
    border-right: 1px solid var(--surface0);
  }

  .hcell {
    position: absolute;
    top: 0;
    height: 100%;
    padding: 0 8px;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 1px;
    overflow: hidden;
    border-right: 1px solid var(--border);
    white-space: nowrap;
  }

  .hcell.num {
    align-items: flex-end;
  }

  .name {
    color: var(--text);
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }

  .type {
    color: var(--faint);
    font-size: 11px;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }

  .resize {
    position: absolute;
    top: 0;
    right: 0;
    width: 6px;
    height: 100%;
    cursor: col-resize;
  }

  .resize:hover {
    background: var(--surface1);
  }

  .gutter {
    position: sticky;
    left: 0;
    z-index: 1;
  }

  .body {
    position: absolute;
  }

  .block {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    will-change: transform;
  }

  .gcell,
  .row {
    height: 22px;
    line-height: 22px;
  }

  .gcell {
    padding-right: 8px;
    text-align: right;
    color: var(--faint);
    background: var(--mantle);
    border-right: 1px solid var(--surface0);
  }

  .row {
    position: relative;
    width: 100%;
    contain: strict;
  }

  .row.odd {
    background: rgb(24 24 37 / 0.55);
  }

  .row:hover {
    background: var(--surface0);
  }

  .cell {
    position: absolute;
    top: 0;
    height: 100%;
    padding: 0 8px;
    overflow: hidden;
    white-space: pre;
    text-overflow: ellipsis;
    border-right: 1px solid var(--border);
    cursor: default;
  }

  .cell.num {
    text-align: right;
  }

  .cell.null {
    color: var(--faint);
    font-style: italic;
  }

  .selection {
    position: absolute;
    top: 0;
    left: 0;
    height: 22px;
    pointer-events: none;
    background: rgb(203 166 247 / 0.14);
    outline: 1px solid var(--accent);
    outline-offset: -1px;
  }
</style>
