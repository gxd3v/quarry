export interface MenuItem {
  label: string
  icon?: string
  hint?: string
  danger?: boolean
  disabled?: boolean
  run: () => void
}

export type MenuEntry = MenuItem | 'separator'

class Menu {
  current = $state<{ x: number; y: number; items: MenuEntry[]; restore: HTMLElement | null } | null>(null)

  // open shows a context menu at the pointer (or below the focused element
  // when opened from the keyboard).
  open(e: MouseEvent, items: MenuEntry[]) {
    e.preventDefault()
    let { clientX: x, clientY: y } = e
    const target = e.currentTarget as HTMLElement | null
    if (x === 0 && y === 0 && target) {
      const r = target.getBoundingClientRect()
      x = r.left + 16
      y = r.bottom
    }
    const restore = document.activeElement instanceof HTMLElement ? document.activeElement : null
    this.current = { x, y, items, restore }
  }

  close() {
    const restore = this.current?.restore
    this.current = null
    restore?.focus()
  }
}

export const menu = new Menu()
