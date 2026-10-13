// startDrag follows the pointer from a pointerdown until release, calling
// move with the offset from the start.
export function startDrag(e: PointerEvent, move: (dx: number, dy: number) => void, end?: () => void) {
  if (e.button !== 0) return
  e.preventDefault()
  const handle = e.currentTarget as HTMLElement
  handle.setPointerCapture(e.pointerId)
  const x0 = e.clientX
  const y0 = e.clientY
  const onMove = (ev: PointerEvent) => move(ev.clientX - x0, ev.clientY - y0)
  const onEnd = () => {
    handle.removeEventListener('pointermove', onMove)
    handle.removeEventListener('pointerup', onEnd)
    handle.removeEventListener('pointercancel', onEnd)
    document.body.classList.remove('dragging')
    end?.()
  }
  document.body.classList.add('dragging')
  handle.addEventListener('pointermove', onMove)
  handle.addEventListener('pointerup', onEnd)
  handle.addEventListener('pointercancel', onEnd)
}
