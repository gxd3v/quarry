// Autocomplete through QueryService.Complete. The server ranks and filters
// (prefix, word boundaries, initials, at most 50 items), so the popup keeps
// its order and asks again on every keystroke; stale requests are aborted.
import type { Completion, CompletionContext, CompletionResult, CompletionSource } from '@codemirror/autocomplete'
import type { EditorState } from '@codemirror/state'

export interface CompleteItem {
  label: string
  insert: string
  detail: string
  kind: string
}

export interface CompleteReply {
  start: number
  end: number
  items: CompleteItem[]
}

export type CompleteFetcher = (script: string, cursor: number, signal: AbortSignal) => Promise<CompleteReply | null>

// Kinds the server sends, mapped to the popup icon type (cm-completionIcon-<type>).
export const KIND_ICONS: Readonly<Record<string, string>> = {
  keyword: 'keyword',
  schema: 'schema',
  table: 'table',
  view: 'view',
  column: 'column',
  function: 'function',
  alias: 'alias',
  snippet: 'snippet',
  collection: 'collection',
  command: 'command',
  key: 'key',
}

// shouldActivate decides whether typing (not Ctrl+Space) opens the popup:
// after an identifier character or a dot, but not inside a number.
export function shouldActivate(before: string): boolean {
  if (before.endsWith('.')) return !/\d\.$/.test(before)
  const m = /[\w$]+$/.exec(before)
  return !!m && !/^\d/.test(m[0])
}

// matchRange finds where the typed prefix matches a label (case-insensitive,
// at its start or at a word start), as [from, to] for highlighting.
export function matchRange(label: string, prefix: string): number[] {
  const p = prefix.replace(/^"/, '').toLowerCase()
  if (!p) return []
  const l = label.toLowerCase()
  if (l.startsWith(p)) return [0, p.length]
  const q = l.startsWith('"') && l.slice(1).startsWith(p) ? 1 : -1
  if (q > 0) return [1, 1 + p.length]
  for (let i = 1; i < l.length; i++) {
    if (/[_.\s]/.test(l[i - 1]) && l.startsWith(p, i)) return [i, i + p.length]
  }
  return []
}

// toResult maps a server reply to a CodeMirror result for the cursor at pos.
export function toResult(reply: CompleteReply, state: Pick<EditorState, 'doc' | 'sliceDoc'>, pos: number): CompletionResult | null {
  if (!reply.items.length) return null
  const len = state.doc.length
  const from = Math.min(Math.max(0, reply.start), pos)
  const to = Math.min(Math.max(pos, reply.end), len)
  const prefix = state.sliceDoc(from, pos)
  const options: Completion[] = reply.items.map((it, i) => ({
    label: it.label,
    apply: it.insert || it.label,
    detail: it.detail || undefined,
    type: KIND_ICONS[it.kind] ?? 'text',
    // Keeps the server's order should anything re-sort.
    boost: -Math.min(i, 99),
  }))
  return { from, to, options, filter: false, getMatch: (c) => matchRange(c.label, prefix) }
}

export function completionSource(fetch: () => CompleteFetcher | undefined): CompletionSource {
  return async (ctx: CompletionContext) => {
    if (!ctx.explicit && !shouldActivate(ctx.state.sliceDoc(Math.max(0, ctx.pos - 64), ctx.pos))) return null
    const f = fetch()
    if (!f) return null
    const ctrl = new AbortController()
    ctx.addEventListener('abort', () => ctrl.abort(), { onDocChange: true })
    try {
      const reply = await f(ctx.state.doc.toString(), ctx.pos, ctrl.signal)
      if (!reply || ctrl.signal.aborted) return null
      return toResult(reply, ctx.state, ctx.pos)
    } catch {
      return null
    }
  }
}
