// A central keymap: actions are registered by id and bound to key chords.
// Chords are written like "Mod+Shift+Enter", where Mod is Ctrl (Cmd on macOS).

export interface Action {
  id: string
  title: string
  // Returns false when the action does not apply, letting the key through.
  run: () => boolean | void
}

export const DEFAULT_BINDINGS: Readonly<Record<string, string>> = {
  'Mod+T': 'tab.new',
  'Mod+W': 'tab.close',
  'Mod+Tab': 'tab.next',
  'Mod+Shift+Tab': 'tab.prev',
  'Mod+PageDown': 'tab.next',
  'Mod+PageUp': 'tab.prev',
  'Mod+Enter': 'run.statement',
  'Mod+Shift+Enter': 'run.script',
  Escape: 'run.cancel',
  'Mod+.': 'run.cancel',
  'Mod+Shift+N': 'connection.new',
  'Alt+1': 'focus.explorer',
  'Alt+2': 'focus.editor',
  'Alt+3': 'focus.results',
}

const MODIFIERS = new Set(['Control', 'Shift', 'Alt', 'Meta'])

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

export interface KeyLike {
  key: string
  code?: string
  ctrlKey: boolean
  shiftKey: boolean
  altKey: boolean
  metaKey: boolean
}

// chordOf normalizes a key event to the chord syntax, or '' for a bare modifier.
export function chordOf(e: KeyLike, mac = isMac): string {
  if (MODIFIERS.has(e.key)) return ''
  let key = e.key
  // Digits and letters by physical key, so Shift or Alt layouts do not change them.
  const code = e.code ?? ''
  if (/^Digit\d$/.test(code)) key = code.slice(5)
  else if (/^Key[A-Z]$/.test(code)) key = code.slice(3)
  else if (key.length === 1) key = key.toUpperCase()
  else if (key === 'Esc') key = 'Escape'
  const parts: string[] = []
  if (mac ? e.metaKey : e.ctrlKey) parts.push('Mod')
  if (mac && e.ctrlKey) parts.push('Ctrl')
  if (e.altKey) parts.push('Alt')
  if (e.shiftKey) parts.push('Shift')
  parts.push(key)
  return parts.join('+')
}

export class Keymap {
  readonly #actions = new Map<string, Action>()
  #bindings = new Map<string, string>()

  constructor(bindings: Readonly<Record<string, string>> = DEFAULT_BINDINGS) {
    this.bind(bindings)
  }

  bind(bindings: Readonly<Record<string, string>>) {
    this.#bindings = new Map(Object.entries(bindings))
  }

  // register adds actions and returns a function that removes them.
  register(...actions: Action[]): () => void {
    for (const a of actions) this.#actions.set(a.id, a)
    return () => {
      for (const a of actions) if (this.#actions.get(a.id) === a) this.#actions.delete(a.id)
    }
  }

  action(id: string): Action | undefined {
    return this.#actions.get(id)
  }

  // keysFor lists the chords bound to an action, for hints and tooltips.
  keysFor(id: string): string[] {
    return [...this.#bindings].filter(([, a]) => a === id).map(([k]) => k)
  }

  run(id: string): boolean {
    const a = this.#actions.get(id)
    return !!a && a.run() !== false
  }

  // handle runs the action bound to the event's chord; true means handled.
  handle(e: KeyLike): boolean {
    const id = this.#bindings.get(chordOf(e))
    return id !== undefined && this.run(id)
  }
}

// label renders a chord for display, e.g. "Ctrl+Shift+Enter".
export function label(chord: string, mac = isMac): string {
  return chord.replace('Mod', mac ? 'Cmd' : 'Ctrl')
}

export const keymap = new Keymap()
