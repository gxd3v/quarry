import { autocompletion } from '@codemirror/autocomplete'
import { EditorState, RangeSetBuilder, StateEffect, StateField, type Extension } from '@codemirror/state'
import { Decoration, EditorView, gutter, GutterMarker, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import { completionSource, type CompleteFetcher } from './complete'
import { baseExtensions } from './setup'

// Script editors live one per tab as EditorStates swapped into one view.
// The app listens to their updates and serves completions through these hooks.
export const scriptHooks: { onUpdate?: (u: ViewUpdate) => void; complete?: CompleteFetcher } = {}

export interface Span {
  from: number
  to: number
}

// setStatement highlights the statement at the cursor (null clears it).
export const setStatement = StateEffect.define<Span | null>()
// setError marks the server error position (null clears it).
export const setError = StateEffect.define<number | null>()

const stmtLine = Decoration.line({ class: 'cm-current-statement' })

const statementField = StateField.define<{ span: Span | null; deco: DecorationSet }>({
  create: () => ({ span: null, deco: Decoration.none }),
  update(v, tr) {
    let span = v.span
    let changed = false
    for (const e of tr.effects) {
      if (e.is(setStatement)) {
        span = e.value
        changed = true
      }
    }
    if (!changed && !tr.docChanged) return v
    if (span && !changed) span = { from: tr.changes.mapPos(span.from, 1), to: tr.changes.mapPos(span.to, -1) }
    if (!span || span.to < span.from) return { span: null, deco: Decoration.none }
    const doc = tr.state.doc
    const b = new RangeSetBuilder<Decoration>()
    const first = doc.lineAt(Math.min(span.from, doc.length)).number
    const last = doc.lineAt(Math.min(span.to, doc.length)).number
    for (let n = first; n <= last; n++) b.add(doc.line(n).from, doc.line(n).from, stmtLine)
    return { span, deco: b.finish() }
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.deco),
})

const errorMark = Decoration.mark({ class: 'cm-error-mark' })

const errorField = StateField.define<{ pos: number; deco: DecorationSet }>({
  create: () => ({ pos: -1, deco: Decoration.none }),
  update(v, tr) {
    for (const e of tr.effects) {
      if (e.is(setError)) {
        const pos = e.value
        if (pos === null || pos < 0) return { pos: -1, deco: Decoration.none }
        const span = tokenAt(tr.state, pos)
        return { pos: span.from, deco: span.to > span.from ? Decoration.set([errorMark.range(span.from, span.to)]) : Decoration.none }
      }
    }
    // Any edit makes the position stale.
    return tr.docChanged && v.pos >= 0 ? { pos: -1, deco: Decoration.none } : v
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.deco),
})

// tokenAt returns the word at pos, or the single character there.
export function tokenAt(state: EditorState, pos: number): Span {
  const len = state.doc.length
  pos = Math.min(Math.max(pos, 0), len)
  const word = state.wordAt(pos)
  if (word) return { from: word.from, to: word.to }
  if (pos < len) return { from: pos, to: pos + 1 }
  return { from: Math.max(0, pos - 1), to: pos }
}

class ErrorMarker extends GutterMarker {
  override toDOM() {
    const el = document.createElement('span')
    el.className = 'cm-error-dot'
    el.title = 'Error'
    return el
  }
}
const errorMarker = new ErrorMarker()

const errorGutter = gutter({
  class: 'cm-error-gutter',
  lineMarker(view, line) {
    const pos = view.state.field(errorField).pos
    return pos >= line.from && pos <= line.to && pos >= 0 ? errorMarker : null
  },
  lineMarkerChange: (u) => u.startState.field(errorField) !== u.state.field(errorField),
})

export function errorPos(state: EditorState): number {
  return state.field(errorField, false)?.pos ?? -1
}

const scriptTheme = EditorView.theme({
  '.cm-current-statement': { backgroundColor: 'color-mix(in srgb, var(--accent) 4%, transparent)' },
  '.cm-current-statement.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent) 7%, transparent)' },
  '.cm-error-mark': {
    textDecoration: 'underline wavy var(--red)',
    textDecorationSkipInk: 'none',
    textUnderlineOffset: '3px',
    backgroundColor: 'color-mix(in srgb, var(--red) 12%, transparent)',
  },
  '.cm-error-gutter': { width: '12px' },
  '.cm-error-gutter .cm-gutterElement': { display: 'flex', alignItems: 'center', justifyContent: 'center' },
  '.cm-error-dot': { width: '7px', height: '7px', borderRadius: '50%', backgroundColor: 'var(--red)' },
})

const extensions: Extension[] = [
  baseExtensions(),
  autocompletion({
    override: [completionSource(() => scriptHooks.complete)],
    activateOnTypingDelay: 120,
    maxRenderedOptions: 60,
    icons: true,
  }),
  errorGutter,
  statementField,
  errorField,
  scriptTheme,
  EditorView.updateListener.of((u) => scriptHooks.onUpdate?.(u)),
]

export function createScriptState(doc = '', cursor = 0): EditorState {
  const state = EditorState.create({ doc, extensions })
  if (!cursor) return state
  return state.update({ selection: { anchor: Math.min(Math.max(0, cursor), state.doc.length) } }).state
}
