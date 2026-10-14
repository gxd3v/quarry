<script lang="ts">
  import { tick } from 'svelte'
  import { menu, type MenuEntry } from '../app/menu.svelte'
  import { notify } from '../app/notify.svelte'
  import { chordOf } from '../keymap'
  import { copyText } from '../ui/clipboard'
  import type { Cell, GridSource } from './buffer'
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

  interface Range {
    r0: number
    r1: number
    c0: number
    c1: number
  }

  interface Props {
    buffer: GridSource
    // Bumped by the owner after appending to the buffer.
    version: number
    // Bumped by the owner when shown rows change in place (edits, filtering).
    epoch?: number
    // Opens a cell in the viewer (double-click, Enter, F3).
    oncell?: (row: number, col: number) => void
    // Table and dialect for SQL INSERT copies.
    copyOptions?: () => CopyOptions
    // Header sorting: a column's direction and rank, and clicks on headers.
    sortOf?: (col: number) => { dir: 'asc' | 'desc'; rank: number } | null
    onsort?: (col: number, add: boolean) => void
    // Extra class names of rows and cells.
    rowClass?: (row: number) => string
    cellClass?: (row: number, col: number) => string
    // In-place editing (double-click, F2, Enter or typing): begin returns the
    // text to edit, or null when the owner handles the cell another way.
    editable?: boolean
    onbeginedit?: (row: number, col: number) => string | null
    // text is null to set NULL.
    oncommitedit?: (row: number, col: number, text: string | null) => void
    // Keys for the owner (Delete, Insert, ...), given the chord; true when handled.
    onkey?: (e: KeyboardEvent, chord: string) => boolean
    // Context menu entries about the selection, shown after "View value".
    menuItems?: () => MenuEntry[]
    // Chords that, while the grid has focus, skip the global shortcuts.
    ownKeys?: string
  }

  let {
    buffer,
    version,
    epoch = 0,
    oncell,
    copyOptions,
    sortOf,
    onsort,
    rowClass,
    cellClass,
    editable = false,
    onbeginedit,
    oncommitedit,
    onkey,
    menuItems,
    ownKeys = '',
  }: Props = $props()

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
    void epoch
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
  let cacheOwner: GridSource | undefined
  let cacheEpoch = 0

  const rows = $derived.by(() => {
    if (cacheOwner !== buffer || cacheEpoch !== epoch) {
      cache = new Map()
      cacheOwner = buffer
      cacheEpoch = epoch
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
    if (!at || at.col < 0) return
    if (editable) beginEdit(at.row, at.col)
    else oncell?.(at.row, at.col)
  }

  // --- in-place editing ---

  let editing = $state<{ row: number; col: number; text: string; initial: string } | null>(null)
  let editInput = $state<HTMLInputElement>()
  const editBox = $derived.by(() => {
    if (!editing || editing.col >= widths.length) return null
    return { x: offsets[editing.col], y: rowTop + (editing.row - rowStart) * ROW_H, w: Math.max(widths[editing.col], 120) }
  })

  // beginEdit edits a cell in place, starting with seed when typing started it.
  export function beginEdit(row: number, col: number, seed?: string) {
    if (!editable || row < 0 || col < 0 || row >= rowCount || col >= colCount) return
    const text = onbeginedit?.(row, col)
    if (text === null || text === undefined) return
    sel = { ar: row, ac: col, hr: row, hc: col }
    reveal(row, col)
    editing = { row, col, text: seed ?? text, initial: text }
    void tick().then(() => {
      if (!editInput) return
      editInput.focus()
      if (seed === undefined) editInput.select()
    })
  }

  // finishEdit ends editing, committing value (undefined cancels) and moving
  // the selection by dr rows and dc columns.
  function finishEdit(value: string | null | undefined, dr = 0, dc = 0) {
    const e = editing
    if (!e) return
    editing = null
    if (value !== undefined && (value === null || value !== e.initial)) oncommitedit?.(e.row, e.col, value)
    scroller.focus()
    if (dr || dc) {
      const hr = Math.min(Math.max(e.row + dr, 0), rowCount - 1)
      const hc = Math.min(Math.max(e.col + dc, 0), colCount - 1)
      sel = { ar: hr, ac: hc, hr, hc }
      reveal(hr, hc)
    }
  }

  function onEditKey(e: KeyboardEvent) {
    e.stopPropagation()
    const chord = chordOf(e)
    if (e.key === 'Enter' && !e.altKey && !e.ctrlKey && !e.metaKey) finishEdit(editing?.text, e.shiftKey ? -1 : 1, 0)
    else if (e.key === 'Tab') finishEdit(editing?.text, 0, e.shiftKey ? -1 : 1)
    else if (e.key === 'Escape') finishEdit(undefined)
    else if (chord === 'Mod+Shift+N') finishEdit(null)
    else return
    e.preventDefault()
  }

  function onContextMenu(e: MouseEvent) {
    const at = cellAt(e.target)
    if (!at) return
    const { r0, r1, c0, c1 } = range
    const inside = at.row >= r0 && at.row <= r1 && (at.col < 0 || (at.col >= c0 && at.col <= c1))
    if (!inside) select(at.row, at.col, false)
    const extra = menuItems?.() ?? []
    const items: MenuEntry[] = [
      { label: 'View value', icon: 'view', hint: editable ? 'F3' : 'Enter', disabled: !oncell || sel.hc < 0, run: () => oncell?.(sel.hr, sel.hc) },
      ...(extra.length ? (['separator', ...extra] as MenuEntry[]) : []),
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

  // selection returns the selected range, or null.
  export function selection(): Range | null {
    return range.r0 >= 0 && range.c0 >= 0 ? { ...range } : null
  }

  // scrollPos also answers after unmount, from the last scroll seen.
  export function scrollPos(): { top: number; left: number } {
    return scroller ? { top: scroller.scrollTop, left: scroller.scrollLeft } : { top: scrollTop, left: scrollLeft }
  }

  export function scrollTo(p: { top: number; left: number }) {
    scroller.scrollTo(p.left, p.top)
  }

  export function focus() {
    scroller.focus()
  }

  function selectAll() {
    if (rowCount && colCount) sel = { ar: 0, ac: 0, hr: rowCount - 1, hc: colCount - 1 }
  }

  function onKeydown(e: KeyboardEvent) {
    const chord = chordOf(e)
    if (onkey?.(e, chord)) {
      e.preventDefault()
      return
    }
    if (rowCount === 0 || colCount === 0) return
    const plain = !e.ctrlKey && !e.metaKey && !e.altKey
    if (chord === 'Mod+C') {
      void copy()
    } else if (chord === 'Mod+A') {
      selectAll()
    } else if (editable && sel.hr >= 0 && ((e.key === 'Enter' && plain && !e.shiftKey) || e.key === 'F2')) {
      beginEdit(sel.hr, sel.hc)
    } else if (editable && sel.hr >= 0 && plain && e.key.length === 1) {
      beginEdit(sel.hr, sel.hc, e.key)
    } else if ((e.key === 'Enter' && plain) || e.key === 'F3') {
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
  data-keys={ownKeys || undefined}
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
        {@const s = sortOf?.(c)}
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (sorting is a mouse shortcut) -->
        <div
          class="hcell"
          class:num={col.numeric}
          class:sortable={!!onsort}
          style:left="{gutter + offsets[c]}px"
          style:width="{widths[c]}px"
          title="{col.name} {col.type}{onsort ? ' (click: sort · Shift+click: add to the sort)' : ''}"
          onclick={(e) => {
            if (onsort && !(e.target as Element).closest('.resize')) onsort(c, e.shiftKey)
          }}
        >
          <span class="name">{col.name}{#if s}<span class="sort" data-dir={s.dir}>{s.dir === 'asc' ? '▲' : '▼'}<sub>{s.rank}</sub></span>{/if}</span><span class="type">{col.type}</span>
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
          <div
            class="gcell {rowClass?.(row.index) ?? ''}"
            class:odd={row.index % 2 === 1}
            class:sel={row.index >= range.r0 && row.index <= range.r1}
            data-r={row.index}
          >
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
          <div class="row {rowClass?.(row.index) ?? ''}" class:odd={row.index % 2 === 1} data-r={row.index}>
            {#each cols as c (c)}
              {@const v = row.cells[c]}
              <div
                class="cell {cellClass?.(row.index, c) ?? ''}"
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
      {#if editing && editBox}
        <input
          class="cell-editor"
          type="text"
          spellcheck="false"
          autocomplete="off"
          data-keys="{ownKeys} Escape"
          style:transform="translate({editBox.x}px, {editBox.y}px)"
          style:width="{editBox.w}px"
          bind:this={editInput}
          bind:value={editing.text}
          onkeydown={onEditKey}
          onblur={() => finishEdit(editing?.text)}
        />
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
    background: var(--bg-main);
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
    background: var(--bg-main);
    border-bottom: 1px solid var(--line-strong);
  }

  .corner {
    position: sticky;
    left: 0;
    z-index: 1;
    height: 100%;
    background: var(--bg-main);
    border-right: 1px solid var(--line);
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
    border-right: 1px solid var(--line);
    white-space: nowrap;
  }

  .hcell.num {
    align-items: flex-end;
  }

  .name {
    color: var(--text-1);
    font-family: var(--font-ui);
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }

  .type {
    color: var(--text-3);
    font-size: 10.5px;
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
    background: color-mix(in srgb, var(--accent) 50%, transparent);
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
    color: var(--overlay0);
    background: var(--bg-main);
    border-right: 1px solid var(--line);
    cursor: default;
  }

  .gcell.sel {
    color: var(--text-1);
    background: var(--bg-active);
  }

  .row {
    position: relative;
    width: 100%;
    contain: strict;
  }

  .row.odd {
    background: color-mix(in srgb, var(--text) 2.5%, transparent);
  }

  .row:hover {
    background: var(--bg-hover);
  }

  .cell {
    position: absolute;
    top: 0;
    height: 100%;
    padding: 0 8px;
    overflow: hidden;
    white-space: pre;
    text-overflow: ellipsis;
    border-right: 1px solid color-mix(in srgb, var(--text) 4%, transparent);
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
    background: color-mix(in srgb, var(--mauve) 10%, transparent);
    outline: 1px solid color-mix(in srgb, var(--mauve) 45%, transparent);
    outline-offset: -1px;
  }

  .hcell.sortable {
    cursor: pointer;
  }

  .hcell.sortable:hover {
    background: var(--bg-hover);
  }

  .sort {
    margin-left: 4px;
    color: var(--accent);
    font-size: 9px;
  }

  .sort sub {
    font-size: 8px;
  }

  .row.new {
    background: color-mix(in srgb, var(--green) 10%, transparent);
  }

  .gcell.new {
    color: var(--green);
    box-shadow: inset 2px 0 0 var(--green);
  }

  .row.deleted {
    background: color-mix(in srgb, var(--red) 10%, transparent);
  }

  .row.deleted .cell {
    color: var(--red);
    text-decoration: line-through;
  }

  .gcell.deleted {
    color: var(--red);
    box-shadow: inset 2px 0 0 var(--red);
  }

  .cell.edited {
    background: color-mix(in srgb, var(--peach) 18%, transparent);
    color: var(--peach);
  }

  .cell.default {
    color: var(--faint);
    font-style: italic;
  }

  .cell-editor {
    position: absolute;
    top: 0;
    left: 0;
    z-index: 1;
    height: 22px;
    padding: 0 7px;
    background: var(--crust);
    border: 1px solid var(--accent);
    border-radius: 0;
    outline: none;
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 12px;
    box-shadow: 0 4px 12px var(--crust);
    user-select: text;
  }

  .selection {
    position: absolute;
    top: 0;
    left: 0;
    height: 22px;
    pointer-events: none;
    background: color-mix(in srgb, var(--mauve) 14%, transparent);
    outline: 1px solid var(--accent);
    outline-offset: -1px;
  }
</style>
