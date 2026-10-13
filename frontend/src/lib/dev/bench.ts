// Self-driving A0 benchmark, started by `quarry --bench-out <file>`. It works
// through the DOM like a user would, so it measures the real webview.

const frame = () => new Promise<number>((r) => requestAnimationFrame(r))
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function $<T extends Element>(sel: string): T {
  const el = document.querySelector<T>(sel)
  if (!el) throw new Error(`bench: ${sel} not found`)
  return el
}

function stats(gaps: number[]) {
  const s = [...gaps].sort((a, b) => a - b)
  const avg = s.reduce((a, b) => a + b, 0) / s.length
  return {
    fps: Math.round(1000 / avg),
    p95: +s[Math.floor(s.length * 0.95)].toFixed(1),
    worst: +s[s.length - 1].toFixed(1),
    over20ms: s.filter((g) => g > 20).length,
  }
}

async function scroll(el: Element, frames: number, move: (n: number) => void) {
  el.scrollTop = 0
  el.scrollLeft = 0
  await frame()
  const gaps: number[] = []
  let last = await frame()
  for (let n = 0; n < frames; n++) {
    move(n)
    const now = await frame()
    gaps.push(now - last)
    last = now
  }
  return stats(gaps)
}

async function idle(frames: number) {
  const gaps: number[] = []
  let last = await frame()
  for (let n = 0; n < frames; n++) {
    const now = await frame()
    gaps.push(now - last)
    last = now
  }
  return stats(gaps)
}

export async function runBench(rows: number): Promise<Record<string, unknown>> {
  const report: Record<string, unknown> = { userAgent: navigator.userAgent, rows }
  report.idle = await idle(120)

  const input = $<HTMLInputElement>('input')
  input.value = String(rows)
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await frame()
  const run = $<HTMLButtonElement>('button.run')
  run.click()
  // Frame pacing while batches arrive and get decoded.
  const gaps: number[] = []
  let last = await frame()
  while (run.disabled) {
    const now = await frame()
    gaps.push(now - last)
    last = now
  }
  report.whileStreaming = stats(gaps)
  await sleep(200)
  report.stream = $<HTMLElement>('.stats').innerText.replace(/\s*\n\s*/g, ' | ')
  report.error = document.querySelector('.error')?.textContent ?? null

  const grid = $<HTMLElement>('.grid')
  report.gridSmallSteps = await scroll(grid, 600, (n) => {
    grid.scrollTop += 66
    grid.scrollLeft = (n % 60) * 15
  })
  report.gridBigJumps = await scroll(grid, 300, () => (grid.scrollTop += 4000))
  report.gridCells = document.querySelectorAll('.cell').length

  ;[...document.querySelectorAll('button')].find((b) => b.textContent?.includes('5k'))?.click()
  await sleep(1000)
  const content = $<HTMLElement>('.cm-content')
  content.focus()
  const keys: number[] = []
  for (let i = 0; i < 200; i++) {
    const t = performance.now()
    document.execCommand('insertText', false, i % 30 === 29 ? '\n' : 'x')
    await frame()
    await frame()
    keys.push(performance.now() - t)
  }
  keys.sort((a, b) => a - b)
  report.typing2Frames = {
    median: +keys[100].toFixed(1),
    p95: +keys[190].toFixed(1),
    worst: +keys[keys.length - 1].toFixed(1),
    docLines: content.closest('.cm-editor')?.querySelectorAll('.cm-gutterElement').length,
  }
  const scroller = $<HTMLElement>('.cm-scroller')
  report.editorScroll = await scroll(scroller, 300, () => (scroller.scrollTop += 300))
  return report
}
