// A central keymap: actions are registered by id and bound to key chords.
// Chords are written like "Mod+Shift+Enter", where Mod is Ctrl (Cmd on macOS).
// User overrides map an action id to one chord ('' unbinds it) and replace
// that action's default chords.

export interface Action {
  id: string
  title: string
  // Palette group, e.g. "File" or "Run".
  category?: string
  // Returns false when the action does not apply, letting the key through.
  run: () => boolean | void
}

// Default chord → action id.
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
  'Mod+N': 'file.newScratch',
  'Mod+O': 'file.open',
  'Mod+S': 'file.save',
  'Mod+Shift+S': 'file.saveAs',
  'Mod+Shift+P': 'palette.commands',
  'Mod+P': 'palette.objects',
  'Mod+Shift+O': 'palette.objects',
  'Mod+Shift+H': 'results.history',
  'Mod+Shift+T': 'terminal.open',
  'Mod+E': 'run.explain',
  'Mod+Shift+E': 'run.explainAnalyze',
}

// Actions whose chords still work while a terminal has the focus; every
// other key goes to the shell (Ctrl+N, Ctrl+P, Ctrl+R... mean something there).
export const TERMINAL_ACTIONS: ReadonlySet<string> = new Set([
  'tab.close',
  'tab.next',
  'tab.prev',
  'focus.explorer',
  'focus.editor',
  'focus.results',
  'palette.commands',
  'terminal.open',
])

// Action id → chord; '' means unbound.
export type Overrides = Readonly<Record<string, string>>

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
  else if (key === ' ') key = 'Space'
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

// normalizeChord parses a hand-written chord ("ctrl+shift+k", "Cmd+Enter")
// into the canonical form, or returns '' when it is not a chord.
export function normalizeChord(s: string): string {
  const parts = s
    .split('+')
    .map((p) => p.trim())
    .filter(Boolean)
  if (!parts.length) return ''
  const mods = new Set<string>()
  let key = ''
  for (const p of parts) {
    switch (p.toLowerCase()) {
      case 'mod':
      case 'cmd':
      case 'command':
      case 'meta':
        mods.add('Mod')
        break
      case 'ctrl':
      case 'control':
        mods.add(isMac ? 'Ctrl' : 'Mod')
        break
      case 'alt':
      case 'option':
        mods.add('Alt')
        break
      case 'shift':
        mods.add('Shift')
        break
      default:
        if (key) return ''
        key = p.length === 1 ? p.toUpperCase() : p[0].toUpperCase() + p.slice(1)
        if (key === 'Esc') key = 'Escape'
    }
  }
  if (!key) return ''
  return [...['Mod', 'Ctrl', 'Alt', 'Shift'].filter((m) => mods.has(m)), key].join('+')
}

// effectiveBindings applies overrides to the defaults: an overridden action
// loses its default chords, and a chord taken by an override is removed from
// the action it was bound to by default.
export function effectiveBindings(defaults: Readonly<Record<string, string>>, overrides: Overrides): Map<string, string> {
  const out = new Map<string, string>()
  const taken = new Set(Object.values(overrides).filter(Boolean))
  for (const [chord, id] of Object.entries(defaults)) {
    if (id in overrides || taken.has(chord)) continue
    out.set(chord, id)
  }
  for (const [id, chord] of Object.entries(overrides)) if (chord) out.set(chord, id)
  return out
}

// cleanOverrides keeps the well-formed entries of user settings, normalizing
// chords; an override equal to the action's only default is dropped.
export function cleanOverrides(raw: unknown, defaults: Readonly<Record<string, string>> = DEFAULT_BINDINGS): Record<string, string> {
  const out: Record<string, string> = {}
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out
  for (const [id, v] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof v !== 'string') continue
    const chord = v.trim() === '' ? '' : normalizeChord(v)
    if (v.trim() !== '' && !chord) continue
    const own = Object.entries(defaults).filter(([, a]) => a === id).map(([k]) => k)
    if (own.length === 1 && own[0] === chord) continue
    out[id] = chord
  }
  return out
}

export class Keymap {
  readonly #actions = new Map<string, Action>()
  readonly #defaults: Readonly<Record<string, string>>
  #overrides: Overrides = {}
  #bindings = new Map<string, string>()
  #listeners = new Set<() => void>()

  constructor(bindings: Readonly<Record<string, string>> = DEFAULT_BINDINGS) {
    this.#defaults = bindings
    this.#bindings = effectiveBindings(bindings, {})
  }

  get overrides(): Overrides {
    return this.#overrides
  }

  setOverrides(o: Overrides) {
    this.#overrides = { ...o }
    this.#bindings = effectiveBindings(this.#defaults, this.#overrides)
    for (const f of this.#listeners) f()
  }

  // onChange calls f after the bindings change; it returns an unsubscribe.
  onChange(f: () => void): () => void {
    this.#listeners.add(f)
    return () => this.#listeners.delete(f)
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

  actions(): Action[] {
    return [...this.#actions.values()]
  }

  // keysFor lists the chords bound to an action, for hints and tooltips.
  keysFor(id: string): string[] {
    return [...this.#bindings].filter(([, a]) => a === id).map(([k]) => k)
  }

  defaultsFor(id: string): string[] {
    return Object.entries(this.#defaults)
      .filter(([, a]) => a === id)
      .map(([k]) => k)
  }

  // boundTo returns the action a chord runs, if any.
  boundTo(chord: string): string | undefined {
    return this.#bindings.get(chord)
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
