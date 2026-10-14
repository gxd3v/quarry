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
      { backgroundColor: 'color-mix(in srgb, var(--surface2) 60%, transparent)' },
    '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--text) 3%, transparent)' },
    '.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--surface2) 35%, transparent)' },
    '&.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--surface1)', outline: '1px solid var(--overlay0)' },
    '&.cm-focused .cm-nonmatchingBracket': { color: 'var(--red)' },
    '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--yellow) 25%, transparent)', outline: '1px solid color-mix(in srgb, var(--yellow) 50%, transparent)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--peach) 40%, transparent)' },
    '.cm-gutters': { backgroundColor: 'var(--base)', color: 'color-mix(in srgb, var(--overlay0) 80%, transparent)', border: 'none' },
    '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--subtext0)' },
    '.cm-lineNumbers .cm-gutterElement': { padding: '0 14px 0 16px' },
    '.cm-panels': { backgroundColor: 'var(--mantle)', color: 'var(--text)', fontFamily: 'var(--font-ui)' },
    '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--line)' },
    '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--line)' },
    '.cm-panel.cm-search input, .cm-panel.cm-search button': {
      backgroundColor: 'var(--surface0)',
      color: 'var(--text)',
      border: '1px solid var(--surface1)',
      borderRadius: '4px',
      backgroundImage: 'none',
    },
    '.cm-panel.cm-search input:focus': { outline: '1px solid var(--accent)' },
    '.cm-tooltip': { backgroundColor: 'var(--mantle)', border: '1px solid var(--surface1)', color: 'var(--text)' },
    '.cm-tooltip.cm-tooltip-autocomplete': {
      padding: '3px',
      borderRadius: '6px',
      boxShadow: '0 8px 24px var(--crust)',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul': {
      fontFamily: 'var(--font-mono)',
      fontSize: '12px',
      maxHeight: '20em',
      minWidth: '220px',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li': {
      display: 'flex',
      alignItems: 'center',
      padding: '1px 8px 1px 2px',
      borderRadius: '4px',
      lineHeight: '1.7',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--surface0)', color: 'var(--text)' },
    '.cm-completionMatchedText': { textDecoration: 'none', color: 'var(--accent)', fontWeight: '600' },
    '.cm-completionDetail': {
      marginLeft: 'auto',
      paddingLeft: '16px',
      color: 'var(--overlay0)',
      fontStyle: 'normal',
      fontSize: '11px',
    },
    '.cm-completionIcon': {
      width: '18px',
      height: '16px',
      marginRight: '6px',
      padding: '0',
      opacity: '1',
      borderRadius: '3px',
      fontSize: '10px',
      fontWeight: '700',
      lineHeight: '16px',
      textAlign: 'center',
      backgroundColor: 'var(--surface0)',
    },
    '.cm-completionIcon-keyword:after': { content: "'K'", color: 'var(--mauve)' },
    '.cm-completionIcon-schema:after': { content: "'S'", color: 'var(--pink)' },
    '.cm-completionIcon-table:after': { content: "'T'", color: 'var(--blue)' },
    '.cm-completionIcon-view:after': { content: "'V'", color: 'var(--teal)' },
    '.cm-completionIcon-column:after': { content: "'C'", color: 'var(--lavender)' },
    '.cm-completionIcon-function:after': { content: "'F'", color: 'var(--yellow)' },
    '.cm-completionIcon-alias:after': { content: "'A'", color: 'var(--sapphire)' },
    '.cm-completionIcon-snippet:after': { content: "'{}'", color: 'var(--green)' },
    '.cm-completionIcon-collection:after': { content: "'D'", color: 'var(--blue)' },
    '.cm-completionIcon-command:after': { content: "'>'", color: 'var(--peach)' },
    '.cm-completionIcon-key:after': { content: "'#'", color: 'var(--flamingo)' },
    '.cm-completionIcon-text:after': { content: "'·'", color: 'var(--overlay1)' },
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
