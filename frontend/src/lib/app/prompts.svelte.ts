// Modal prompts the stores can await. Only one prompt is shown at a time;
// later requests wait for the earlier ones.

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

export interface Choice<T extends string> {
  value: T
  label: string
  primary?: boolean
  danger?: boolean
}

export interface ChoiceRequest {
  title: string
  text: string
  choices: Choice<string>[]
  // null when dismissed.
  resolve: (value: string | null) => void
}

export interface InputRequest {
  title: string
  label: string
  value: string
  placeholder: string
  suggestions: string[]
  confirm: string
  resolve: (value: string | null) => void
}

export interface RiskEntry {
  // e.g. "UPDATE", "2 rows deleted".
  title: string
  detail: string
  // Statement text, shown in mono.
  code: string
}

// A confirmation that lists what is about to happen, e.g. the risky
// statements of a run or the changes of an Apply.
export interface ReviewRequest {
  title: string
  text: string
  items: RiskEntry[]
  confirm: string
  env: string
  resolve: (ok: boolean) => void
}

export interface ParamField {
  // Bare name, the key of RunRequest.params.
  name: string
  // As written in the script, e.g. "$1" or ":id".
  label: string
}

export interface ParamsRequest {
  title: string
  fields: ParamField[]
  values: Record<string, string>
  // null when dismissed.
  resolve: (values: Record<string, string> | null) => void
}

class Prompts {
  password = $state<PasswordRequest | null>(null)
  confirm = $state<ConfirmRequest | null>(null)
  choice = $state<ChoiceRequest | null>(null)
  input = $state<InputRequest | null>(null)
  review = $state<ReviewRequest | null>(null)
  params = $state<ParamsRequest | null>(null)
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

  // choose offers several buttons; it resolves to the picked value, or null.
  choose<T extends string>(opts: { title: string; text: string; choices: Choice<T>[] }): Promise<T | null> {
    return this.#serial(
      () =>
        new Promise<T | null>((resolve) => {
          this.choice = {
            title: opts.title,
            text: opts.text,
            choices: opts.choices,
            resolve: (v) => {
              this.choice = null
              resolve(v as T | null)
            },
          }
        }),
    )
  }

  // prompt asks for a line of text; null when dismissed.
  prompt(opts: { title: string; label?: string; value?: string; placeholder?: string; suggestions?: string[]; confirm?: string }): Promise<string | null> {
    return this.#serial(
      () =>
        new Promise<string | null>((resolve) => {
          this.input = {
            title: opts.title,
            label: opts.label ?? '',
            value: opts.value ?? '',
            placeholder: opts.placeholder ?? '',
            suggestions: opts.suggestions ?? [],
            confirm: opts.confirm ?? 'OK',
            resolve: (v) => {
              this.input = null
              resolve(v)
            },
          }
        }),
    )
  }

  // reviewList asks to confirm a list of risky operations.
  reviewList(opts: { title: string; text?: string; items: RiskEntry[]; confirm?: string; env?: string }): Promise<boolean> {
    return this.#serial(
      () =>
        new Promise<boolean>((resolve) => {
          this.review = {
            title: opts.title,
            text: opts.text ?? '',
            items: opts.items,
            confirm: opts.confirm ?? 'Run anyway',
            env: opts.env ?? '',
            resolve: (ok) => {
              this.review = null
              resolve(ok)
            },
          }
        }),
    )
  }

  // askParams asks for bind parameter values, prefilled with values.
  askParams(opts: { title: string; fields: ParamField[]; values: Record<string, string> }): Promise<Record<string, string> | null> {
    return this.#serial(
      () =>
        new Promise<Record<string, string> | null>((resolve) => {
          this.params = {
            title: opts.title,
            fields: opts.fields,
            values: opts.values,
            resolve: (v) => {
              this.params = null
              resolve(v)
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
