<script lang="ts">
  import { menu, type MenuEntry } from '../app/menu.svelte'
  import { notify } from '../app/notify.svelte'
  import { chordOf } from '../keymap'
  import { copyText } from '../ui/clipboard'
  import type { Cell, ResultBuffer } from './buffer'
  import { COPY_FORMATS, copyPlain, formatCopy, type CopyFormat, type CopyOptions } from './copy'
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
    // Opens a cell in the viewer (double-click, Enter, F3).
    oncell?: (row: number, col: number) => void
    // Table and dialect for SQL INSERT copies.
    copyOptions?: () => CopyOptions
  }

  let { buffer, version, oncell, copyOptions }: Props = $props()

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
  const colCount = $derived(buffer.columns.length)

  // Writable deriveds: reset when the buffer changes, assigned on resize/click.
  let widths = $derived.by(() => {
    const sample: Cell[][] = []
    for (let i = 0; i < Math.min(buffer.length, SAMPLE_ROWS); i++) sample.push(buffer.row(i))
    return estimateWidths(buffer.columns, sample)
  })
  // Selection: the anchor cell and the head (active) cell span a range.
  let sel = $derived.by(() => {
    void buffer
    return { ar: -1, ac: -1, hr: -1, hc: -1 }
  })
  const range = $derived({
    r0: Math.min(sel.ar, sel.hr),
    r1: Math.max(sel.ar, sel.hr),
    c0: Math.min(sel.ac, sel.hc),
    c1: Math.max(sel.ac, sel.hc),
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

  // The visible part of the selected range, and the active cell.
  const rangeBox = $derived.by(() => {
    const { r0, r1, c0, c1 } = range
    if (r0 < 0 || c0 < 0 || c1 >= widths.length || (r0 === r1 && c0 === c1)) return null
    const top = Math.max(r0, rowStart)
    const bottom = Math.min(r1, rowEnd - 1)
    if (top > bottom) return null
    return {
      x: offsets[c0],
      y: rowTop + (top - rowStart) * ROW_H,
      w: offsets[c1 + 1] - offsets[c0],
      h: (bottom - top + 1) * ROW_H,
    }
  })
  const headBox = $derived.by(() => {
    if (sel.hr < rowStart || sel.hr >= rowEnd || sel.hc < 0 || sel.hc >= widths.length) return null
    return { x: offsets[sel.hc], y: rowTop + (sel.hr - rowStart) * ROW_H, w: widths[sel.hc] }
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

  // cellAt reads the cell (col -1 for the row number gutter) under an element.
  function cellAt(target: EventTarget | null): { row: number; col: number } | null {
    const el = target instanceof Element ? target.closest<HTMLElement>('.cell, .gcell') : null
    if (!el) return null
    if (el.classList.contains('gcell')) return { row: Number(el.dataset.r), col: -1 }
    const row = el.parentElement
    return row ? { row: Number(row.dataset.r), col: Number(el.dataset.c) } : null
  }

  function select(row: number, col: number, extend: boolean) {
    const whole = col < 0
    if (extend && sel.ar >= 0) {
      sel = whole ? { ...sel, hr: row, hc: sel.ac === 0 ? colCount - 1 : 0 } : { ...sel, hr: row, hc: col }
    } else {
      sel = whole ? { ar: row, ac: 0, hr: row, hc: colCount - 1 } : { ar: row, ac: col, hr: row, hc: col }
    }
  }

  function onPointerDown(e: PointerEvent) {
    if (e.button !== 0) return
    const at = cellAt(e.target)
    if (!at) return
    select(at.row, at.col, e.shiftKey)
    const whole = at.col < 0
    const move = (ev: PointerEvent) => {
      const to = cellAt(document.elementFromPoint(ev.clientX, ev.clientY))
      if (!to) return
      if (whole) sel = { ...sel, hr: to.row }
      else if (to.col >= 0) sel = { ...sel, hr: to.row, hc: to.col }
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  function onDblClick(e: MouseEvent) {
    const at = cellAt(e.target)
    if (at && at.col >= 0) oncell?.(at.row, at.col)
  }

  function onContextMenu(e: MouseEvent) {
    const at = cellAt(e.target)
    if (!at) return
    const { r0, r1, c0, c1 } = range
    const inside = at.row >= r0 && at.row <= r1 && (at.col < 0 || (at.col >= c0 && at.col <= c1))
    if (!inside) select(at.row, at.col, false)
    const items: MenuEntry[] = [
      { label: 'View value', icon: 'view', hint: 'Enter', disabled: !oncell || sel.hc < 0, run: () => oncell?.(sel.hr, sel.hc) },
      'separator',
      { label: 'Copy', icon: 'copy', hint: 'Ctrl+C', run: () => void copy() },
      { label: 'Copy with headers', run: () => void copy('tsv') },
      ...COPY_FORMATS.filter((f) => f.format !== 'tsv').map((f) => ({ label: `Copy as ${f.label}`, run: () => void copy(f.format) })),
      'separator',
      { label: 'Select all', hint: 'Ctrl+A', run: selectAll },
    ]
    menu.open(e, items)
  }

  // copy puts the selected range on the clipboard: plain text (TSV without a
  // header) or the given format.
  export async function copy(format?: CopyFormat): Promise<string> {
    const { r0, r1, c0, c1 } = range
    if (r0 < 0 || c0 < 0) return ''
    const cells = buffer.slice(r0, r1, c0, c1)
    const text = format ? formatCopy(format, buffer.columns.slice(c0, c1 + 1), cells, copyOptions?.() ?? {}) : copyPlain(cells)
    try {
      await copyText(text)
      const n = r1 - r0 + 1
      notify.flash(`Copied ${n === 1 && c0 === c1 ? 'a cell' : `${n.toLocaleString('en-US')} ${n === 1 ? 'row' : 'rows'}`}`)
    } catch (err) {
      notify.error(err, 'Copy')
    }
    return text
  }

  // The active cell, or null.
  export function active(): { row: number; col: number } | null {
    return sel.hr >= 0 && sel.hc >= 0 ? { row: sel.hr, col: sel.hc } : null
  }

  // setSelection selects a range programmatically (anchor and head cells).
  export function setSelection(ar: number, ac: number, hr = ar, hc = ac) {
    sel = { ar, ac, hr, hc }
    reveal(hr, hc)
  }

  function selectAll() {
    if (rowCount && colCount) sel = { ar: 0, ac: 0, hr: rowCount - 1, hc: colCount - 1 }
  }

  function onKeydown(e: KeyboardEvent) {
    if (rowCount === 0 || colCount === 0) return
    const chord = chordOf(e)
    if (chord === 'Mod+C') {
      void copy()
    } else if (chord === 'Mod+A') {
      selectAll()
    } else if ((e.key === 'Enter' && !e.ctrlKey && !e.metaKey && !e.altKey) || e.key === 'F3') {
      const a = active()
      if (!a || !oncell) return
      oncell(a.row, a.col)
    } else {
      const page = Math.max(1, Math.floor(bodyHeight / ROW_H) - 1)
      const mod = e.ctrlKey || e.metaKey
      let { hr, hc } = sel.hr < 0 ? { hr: 0, hc: 0 } : sel
      switch (e.key) {
        case 'ArrowUp':
          hr = mod ? 0 : hr - 1
          break
        case 'ArrowDown':
          hr = mod ? rowCount - 1 : hr + 1
          break
        case 'ArrowLeft':
          hc = mod ? 0 : hc - 1
          break
        case 'ArrowRight':
          hc = mod ? colCount - 1 : hc + 1
          break
        case 'PageUp':
          hr -= page
          break
        case 'PageDown':
          hr += page
          break
        case 'Home':
          hc = 0
          if (mod) hr = 0
          break
        case 'End':
          hc = colCount - 1
          if (mod) hr = rowCount - 1
          break
        default:
          return
      }
      hr = Math.min(Math.max(hr, 0), rowCount - 1)
      hc = Math.min(Math.max(hc, 0), colCount - 1)
      sel = e.shiftKey && sel.ar >= 0 ? { ...sel, hr, hc } : { ar: hr, ac: hc, hr, hc }
      reveal(hr, hc)
    }
    e.preventDefault()
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
    e.stopPropagation()
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
  onpointerdown={onPointerDown}
  ondblclick={onDblClick}
  oncontextmenu={onContextMenu}
  onkeydown={onKeydown}
  role="grid"
  tabindex="0"
  aria-rowcount={rowCount}
  aria-colcount={colCount}
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
          <div class="gcell" class:odd={row.index % 2 === 1} class:sel={row.index >= range.r0 && row.index <= range.r1} data-r={row.index}>
            {row.index + 1}
          </div>
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
      {#if rangeBox}
        <div
          class="range"
          style:transform="translate({rangeBox.x}px, {rangeBox.y}px)"
          style:width="{rangeBox.w}px"
          style:height="{rangeBox.h}px"
        ></div>
      {/if}
      {#if headBox}
        <div
          class="selection"
          style:transform="translate({headBox.x}px, {headBox.y}px)"
          style:width="{headBox.w}px"
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
    user-select: none;
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
    cursor: default;
  }

  .gcell.sel {
    color: var(--subtext1);
    background: var(--surface0);
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

  .range {
    position: absolute;
    top: 0;
    left: 0;
    pointer-events: none;
    background: rgb(203 166 247 / 0.1);
    outline: 1px solid rgb(203 166 247 / 0.45);
    outline-offset: -1px;
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
