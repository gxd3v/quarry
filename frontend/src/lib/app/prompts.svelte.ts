// Modal prompts the stores can await. Only one of each kind is shown at a
// time; later requests wait for the earlier ones.

export interface PasswordRequest {
  title: string
  error: string
  resolve: (password: string | null) => void
}

export interface ConfirmRequest {
  title: string
  text: string
  confirm: string
  danger: boolean
  // Connection environment to highlight, if the question is about one.
  env: string
  resolve: (ok: boolean) => void
}

class Prompts {
  password = $state<PasswordRequest | null>(null)
  confirm = $state<ConfirmRequest | null>(null)
  #queue: Promise<unknown> = Promise.resolve()

  // askPassword resolves to the typed password, or null when dismissed.
  askPassword(title: string, error = ''): Promise<string | null> {
    return this.#serial(
      () =>
        new Promise<string | null>((resolve) => {
          this.password = {
            title,
            error,
            resolve: (pw) => {
              this.password = null
              resolve(pw)
            },
          }
        }),
    )
  }

  ask(opts: { title: string; text: string; confirm?: string; danger?: boolean; env?: string }): Promise<boolean> {
    return this.#serial(
      () =>
        new Promise<boolean>((resolve) => {
          this.confirm = {
            title: opts.title,
            text: opts.text,
            confirm: opts.confirm ?? 'OK',
            danger: opts.danger ?? false,
            env: opts.env ?? '',
            resolve: (ok) => {
              this.confirm = null
              resolve(ok)
            },
          }
        }),
    )
  }

  #serial<T>(f: () => Promise<T>): Promise<T> {
    const next = this.#queue.then(f, f)
    this.#queue = next.catch(() => {})
    return next
  }
}

export const prompts = new Prompts()
