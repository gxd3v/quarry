// The last text copied, for dev tooling (smoke tests read it).
export const clipboard = { last: '' }

// copyText writes text to the system clipboard, falling back to a hidden
// textarea where the async clipboard API is not allowed.
export async function copyText(text: string): Promise<void> {
  clipboard.last = text
  try {
    await navigator.clipboard.writeText(text)
    return
  } catch {
    // Fall through.
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  const focus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.body.append(ta)
  ta.select()
  const ok = document.execCommand('copy')
  ta.remove()
  focus?.focus()
  if (!ok) throw new Error('the clipboard is not available')
}
