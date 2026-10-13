import { HighlightStyle, syntaxHighlighting, syntaxTree } from '@codemirror/language'
import { RangeSetBuilder, type Extension } from '@codemirror/state'
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'

const theme = EditorView.theme(
  {
    '&': {
      height: '100%',
      color: 'var(--text)',
      backgroundColor: 'var(--base)',
      fontSize: '13px',
    },
    '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.5' },
    '.cm-content': { caretColor: 'var(--rosewater)' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--rosewater)', borderLeftWidth: '2px' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
      { backgroundColor: 'rgb(88 91 112 / 0.6)' },
    '.cm-activeLine': { backgroundColor: 'rgb(49 50 68 / 0.45)' },
    '.cm-selectionMatch': { backgroundColor: 'rgb(88 91 112 / 0.35)' },
    '&.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--surface1)', outline: '1px solid var(--overlay0)' },
    '&.cm-focused .cm-nonmatchingBracket': { color: 'var(--red)' },
    '.cm-searchMatch': { backgroundColor: 'rgb(249 226 175 / 0.25)', outline: '1px solid rgb(249 226 175 / 0.5)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'rgb(250 179 135 / 0.4)' },
    '.cm-gutters': { backgroundColor: 'var(--mantle)', color: 'var(--overlay0)', border: 'none' },
    '.cm-activeLineGutter': { backgroundColor: 'var(--surface0)', color: 'var(--lavender)' },
    '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 12px' },
    '.cm-panels': { backgroundColor: 'var(--mantle)', color: 'var(--text)' },
    '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--surface0)' },
    '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--surface0)' },
    '.cm-panel.cm-search input, .cm-panel.cm-search button': {
      backgroundColor: 'var(--surface0)',
      color: 'var(--text)',
      border: '1px solid var(--surface1)',
      borderRadius: '4px',
      backgroundImage: 'none',
    },
    '.cm-panel.cm-search input:focus': { outline: '1px solid var(--accent)' },
    '.cm-tooltip': { backgroundColor: 'var(--mantle)', border: '1px solid var(--surface1)', color: 'var(--text)' },
    '.cm-sql-fn, .cm-sql-fn span': { color: 'var(--blue)' },
    '.cm-sql-param, .cm-sql-param span': { color: 'var(--maroon)' },
  },
  { dark: true },
)

const highlight = HighlightStyle.define([
  { tag: t.keyword, color: 'var(--mauve)' },
  { tag: [t.typeName, t.standard(t.typeName)], color: 'var(--yellow)' },
  { tag: [t.function(t.name), t.standard(t.name)], color: 'var(--blue)' },
  { tag: t.string, color: 'var(--green)' },
  { tag: [t.number, t.bool, t.null], color: 'var(--peach)' },
  { tag: t.comment, color: 'var(--overlay1)', fontStyle: 'italic' },
  { tag: t.operator, color: 'var(--sky)' },
  { tag: t.special(t.string), color: 'var(--flamingo)' },
  { tag: t.special(t.name), color: 'var(--maroon)' },
  { tag: [t.punctuation, t.paren, t.bracket, t.brace], color: 'var(--overlay2)' },
  { tag: t.invalid, color: 'var(--red)' },
])

const fnMark = Decoration.mark({ class: 'cm-sql-fn' })
const paramMark = Decoration.mark({ class: 'cm-sql-param' })

// The SQL grammar has no call or parameter nodes, so `name(` and `$1` are
// recognised from adjacent tokens in the visible part of the tree.
function decorate(view: EditorView): DecorationSet {
  const b = new RangeSetBuilder<Decoration>()
  const tree = syntaxTree(view.state)
  for (const { from, to } of view.visibleRanges) {
    tree.iterate({
      from,
      to,
      enter: (n) => {
        if (n.name === 'Identifier' || n.name === 'Keyword') {
          const next = n.node.nextSibling
          if (next?.name === 'Parens' && next.from === n.to) b.add(n.from, n.to, fnMark)
        } else if (n.type.isError && view.state.sliceDoc(n.from, n.to) === '$') {
          const next = n.node.nextSibling
          if (next?.name === 'Number' && next.from === n.to) b.add(n.from, next.to, paramMark)
        }
      },
    })
  }
  return b.finish()
}

const callsAndParams = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: EditorView) {
      this.decorations = decorate(view)
    }
    update(u: ViewUpdate) {
      if (u.docChanged || u.viewportChanged || syntaxTree(u.startState) !== syntaxTree(u.state)) {
        this.decorations = decorate(u.view)
      }
    }
  },
  { decorations: (v) => v.decorations },
)

export const mocha: Extension = [theme, syntaxHighlighting(highlight), callsAndParams]
