import { Cancelled, errorMessage, isAbort } from '../api/errors'

export type ToastKind = 'error' | 'info' | 'success'

export interface ToastAction {
  label: string
  run: () => void
}

export interface Toast {
  id: number
  kind: ToastKind
  text: string
  actions: ToastAction[]
}

class Notifier {
  toasts = $state<Toast[]>([])
  // Transient status bar message.
  status = $state('')
  #seq = 0
  #statusTimer: ReturnType<typeof setTimeout> | undefined

  toast(kind: ToastKind, text: string, ttl = kind === 'error' ? 8000 : 4000, actions: ToastAction[] = []) {
    const id = ++this.#seq
    this.toasts = [...this.toasts.slice(-4), { id, kind, text, actions }]
    if (ttl > 0) setTimeout(() => this.dismiss(id), ttl)
  }

  // error reports a failed action, ignoring aborts and dismissed prompts.
  error(err: unknown, what = '') {
    if (err instanceof Cancelled || isAbort(err)) return
    const msg = errorMessage(err)
    this.toast('error', what ? `${what}: ${msg}` : msg)
  }

  dismiss(id: number) {
    this.toasts = this.toasts.filter((t) => t.id !== id)
  }

  flash(text: string, ttl = 4000) {
    this.status = text
    clearTimeout(this.#statusTimer)
    this.#statusTimer = setTimeout(() => (this.status = ''), ttl)
  }
}

export const notify = new Notifier()
