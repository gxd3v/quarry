import { Text, type EditorState, type TransactionSpec } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import type { Row } from '../gen/quarry/v1/common_pb'
import { dataClient, queryClient, workspaceClient } from '../api/clients'
import { errorMessage, isAbort, needsConfirmation, notConnected, tabBusy, unimplemented, unknownTab } from '../api/errors'
import type { CompleteReply } from '../editor/complete'
import { createScriptState, errorPos, setError, setStatement } from '../editor/script'
import { statementAt, type Range } from '../editor/statements'
import { RunMode, type PlanNode } from '../gen/quarry/v1/query_pb'
import { decide, paramLabel } from '../run/guard'
import { RunModel, type ResultSet, type TxInfo } from '../run/model'
import type { ScopeLike } from '../sql'
import { connections } from './connections.svelte'
import { afterClose, cycle } from './layout'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'
import type { TerminalSession } from '../terminal/session.svelte'
import type { TableState } from './table.svelte'
import { slicer } from './yield'

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// Messages view in the results panel.
export const MESSAGES = -1

// Results panel view showing the connection's query history.
export const HISTORY = -2

// Results panel view showing the last explained plan.
export const PLAN = -3

export interface PlanResult {
  root: PlanNode | null
  analyze: boolean
  statement: string
  error: string
}

export interface TabOptions {
  // File or scratch path; '' for an untitled script.
  path?: string
  title?: string
  scratch?: boolean
  // Content last saved, which decides the dirty marker; defaults to the text.
  saved?: string
  cursor?: number
  // Makes a table tab showing this table.
  table?: TableState
  // Makes a terminal tab running this shell.
  terminal?: TerminalSession
}

export class Tab {
  readonly key: number
  // A script editor, a table view (see table.svelte.ts) or a terminal.
  readonly kind: 'script' | 'table' | 'terminal'
  readonly table: TableState | null
  readonly terminal: TerminalSession | null
  title = $state('')
  path = $state('')
  scratch = $state(false)
  // The editor content differs from what was last saved.
  dirty = $state(false)
  savedDoc: Text
  connId = $state('')
  scope = $state<ScopeLike>({ database: '', schema: '' })
  language = $state('sql')
  run = $state.raw(new RunModel())
  // Bumped (at most once per frame) when run changes.
  version = $state(0)
  busy = $state<'' | 'run' | 'fetch'>('')
  startedAt = $state(0)
  canceling = $state(false)
  tx = $state<TxInfo>({ supported: true, autoCommit: true, inTx: false })
  // Selected result, or MESSAGES, HISTORY or PLAN.
  view = $state(0)
  plan = $state.raw<PlanResult | null>(null)
  // Last bind parameter values, by bare name.
  params: Record<string, string> = {}

  // The editor state while the tab is not shown (the view holds it otherwise).
  editor: EditorState
  serverId: string | null = null
  controller: AbortController | null = null
  opening: Promise<string> | null = null
  // Statement ranges of the last analyzed document.
  analyzed: { doc: EditorState['doc']; ranges: Range[] } | null = null
  #frame = 0

  constructor(key: number, connId: string, scope: ScopeLike, text: string, opts: TabOptions = {}) {
    this.key = key
    this.kind = opts.table ? 'table' : opts.terminal ? 'terminal' : 'script'
    this.table = opts.table ?? null
    this.terminal = opts.terminal ?? null
    this.path = opts.path ?? ''
    this.title = opts.title || `Script ${key}`
    this.scratch = opts.scratch ?? false
    this.connId = connId
    this.scope = scope
    this.editor = createScriptState(text, opts.cursor ?? 0)
    this.savedDoc = opts.saved === undefined ? this.editor.doc : Text.of(opts.saved.split(/\r\n?|\n/))
    this.dirty = !sameDoc(this.editor.doc, this.savedDoc)
  }

  touch() {
    if (this.#frame) return
    this.#frame = requestAnimationFrame(() => {
      this.#frame = 0
      this.version++
    })
  }
}

function sameDoc(a: Text, b: Text): boolean {
  return a === b || (a.length === b.length && a.eq(b))
}

export interface RunTarget {
  mode: RunMode
  start: number
  end: number
}

// The single script editor view; the tab it shows owns its live state.
export const editorHost: { view: EditorView | null; shown: Tab | null } = { view: null, shown: null }

class Tabs {
  tabs = $state<Tab[]>([])
  activeKey = $state(0)
  readonly active: Tab | null = $derived(this.tabs.find((t) => t.key === this.activeKey) ?? null)
  #seq = 0

  open(connId: string, scope: ScopeLike = { database: '', schema: '' }, text = '', opts: TabOptions = {}): Tab {
    const tab = new Tab(++this.#seq, connId, scope, text, opts)
    this.tabs.push(tab)
    this.activeKey = tab.key
    if (tab.kind !== 'terminal') this.#server(tab).catch((err: unknown) => notify.error(err, 'Open tab'))
    return tab
  }

  byPath(path: string): Tab | undefined {
    const norm = (p: string) => p.replaceAll('\\', '/').toLowerCase()
    return this.tabs.find((t) => t.path && norm(t.path) === norm(path))
  }

  // docChanged refreshes the dirty marker after an edit.
  docChanged(tab: Tab, doc = this.state(tab).doc) {
    const dirty = !sameDoc(doc, tab.savedDoc)
    if (tab.dirty !== dirty) tab.dirty = dirty
  }

  // markSaved records doc as the saved content.
  markSaved(tab: Tab, doc: Text) {
    tab.savedDoc = doc
    this.docChanged(tab)
  }

  activate(tab: Tab) {
    this.activeKey = tab.key
  }

  step(dir: number) {
    const i = this.tabs.findIndex((t) => t.key === this.activeKey)
    const next = this.tabs[cycle(i, this.tabs.length, dir)]
    if (next) this.activeKey = next.key
  }

  async close(tab: Tab) {
    if (tab.tx.inTx) {
      const ok = await prompts.ask({
        title: 'Close tab',
        text: `${tab.title} has an open transaction on ${connections.name(tab.connId)}. Closing it rolls the transaction back.`,
        confirm: 'Roll back and close',
        danger: true,
      })
      if (!ok) return
    }
    tab.controller?.abort()
    tab.terminal?.dispose()
    const i = this.tabs.indexOf(tab)
    if (i < 0) return
    const n = this.tabs.length
    this.tabs.splice(i, 1)
    if (this.activeKey === tab.key) this.activeKey = this.tabs[afterClose(i, n)]?.key ?? 0
    if (editorHost.shown === tab) editorHost.shown = null
    const id = tab.serverId
    tab.serverId = null
    if (id) queryClient().closeTab({ tabId: id }).catch(() => {})
  }

  // state returns the live editor state of a tab.
  state(tab: Tab): EditorState {
    return editorHost.shown === tab && editorHost.view ? editorHost.view.state : tab.editor
  }

  update(tab: Tab, spec: TransactionSpec) {
    if (editorHost.shown === tab && editorHost.view) {
      editorHost.view.dispatch(spec)
      return
    }
    const tr = tab.editor.update(spec)
    tab.editor = tr.state
    if (tr.docChanged) this.docChanged(tab)
  }

  // target computes what Ctrl+Enter (whole=false) or Ctrl+Shift+Enter runs.
  target(tab: Tab, whole: boolean): RunTarget {
    const st = this.state(tab)
    if (whole) return { mode: RunMode.RANGE, start: 0, end: st.doc.length }
    const sel = st.selection.main
    if (!sel.empty) return { mode: RunMode.RANGE, start: sel.from, end: sel.to }
    return { mode: RunMode.STATEMENT, start: sel.head, end: sel.head }
  }

  async rebind(tab: Tab, connId: string, scope: ScopeLike) {
    if (tab.tx.inTx) {
      const ok = await prompts.ask({
        title: 'Change connection',
        text: 'This tab has an open transaction. Rebinding closes its session and rolls the transaction back.',
        confirm: 'Roll back and rebind',
        danger: true,
      })
      if (!ok) return
    }
    this.#abort(tab)
    const id = await this.#server(tab)
    try {
      const res = await this.#retryBusy(() => queryClient().rebind({ tabId: id, connId, scope }))
      tab.language = res.language
    } catch (err) {
      if (!unknownTab(err)) throw err
      tab.serverId = null
    }
    tab.connId = connId
    tab.scope = { ...scope }
    tab.tx = { supported: true, autoCommit: true, inTx: false }
    tab.analyzed = null
    if (!tab.serverId) await this.#server(tab)
    if (tab.path) {
      await workspaceClient().bind({ path: tab.path, binding: { connId, scope: { database: scope.database, schema: scope.schema } } })
    }
  }

  // complete asks the server for completions; it never connects, so it
  // cannot prompt for a password while typing.
  async complete(tab: Tab, script: string, cursor: number, signal: AbortSignal): Promise<CompleteReply | null> {
    for (let attempt = 0; attempt < 2; attempt++) {
      const id = await this.#server(tab)
      try {
        const res = await queryClient().complete({ tabId: id, script, cursor }, { signal })
        return { start: res.start, end: res.end, items: res.items }
      } catch (err) {
        if (!unknownTab(err) || attempt > 0) throw err
        if (tab.serverId === id) tab.serverId = null
      }
    }
    return null
  }

  async run(tab: Tab, target: RunTarget) {
    this.#abort(tab)
    const ctrl = new AbortController()
    tab.controller = ctrl
    const st = this.state(tab)
    const script = st.doc.toString()
    const doc = st.doc
    const model = new RunModel(undefined, script)
    tab.run = model
    tab.view = 0
    tab.busy = 'run'
    tab.canceling = false
    tab.startedAt = performance.now()
    if (errorPos(st) >= 0) this.update(tab, { effects: setError.of(null) })
    tab.touch()

    let got = false
    let confirmed = false
    let params: Record<string, string> = {}
    try {
      const pre = await this.#preflight(tab, script, target, ctrl.signal)
      if (pre.stop) {
        if (pre.error) {
          model.messages.push({ kind: 'error', text: pre.error })
          model.errors++
        } else {
          model.info(pre.info)
        }
        model.finish(0, false)
        return
      }
      confirmed = pre.confirmed
      params = pre.params
      tab.startedAt = performance.now()
      for (;;) {
        try {
          await this.#call(tab, async (tabId) => {
            const slice = slicer()
            const req = { tabId, script, mode: target.mode, start: target.start, end: target.end, confirmed, params }
            for await (const ev of queryClient().run(req, { signal: ctrl.signal })) {
              got = true
              model.apply(ev)
              tab.touch()
              await slice()
            }
          }, () => !got)
          break
        } catch (err) {
          if (got || confirmed || !needsConfirmation(err) || ctrl.signal.aborted) throw err
          const env = connections.env(tab.connId)
          const ok = await prompts.ask({
            title: env === 'prod' ? `Run on PROD: ${connections.name(tab.connId)}` : 'Confirm run',
            text: errorMessage(err).replace(/^confirmation required:\s*/, ''),
            confirm: 'Run anyway',
            danger: true,
            env,
          })
          if (!ok || ctrl.signal.aborted) {
            model.info('Run not confirmed')
            model.finish(0, false)
            break
          }
          confirmed = true
          tab.startedAt = performance.now()
        }
      }
    } catch (err) {
      if (!ctrl.signal.aborted && !isAbort(err)) {
        model.messages.push({ kind: 'error', text: errorMessage(err) })
        model.errors++
      }
    } finally {
      this.#end(tab, ctrl, model)
      if (tab.run === model) {
        if (model.errors > 0 || (model.results.length === 0 && model.messages.length)) tab.view = MESSAGES
        if (model.errorAt >= 0 && this.state(tab).doc === doc) this.markError(tab, model.errorAt)
      }
    }
  }

  // explain shows the plan of the statement at target; analyze executes it
  // (the server rolls it back), so only then do the run guards apply.
  async explain(tab: Tab, target: RunTarget, analyze: boolean) {
    if (tab.busy) return
    const ctrl = new AbortController()
    tab.controller = ctrl
    const st = this.state(tab)
    const script = st.doc.toString()
    const doc = st.doc
    tab.busy = 'run'
    tab.canceling = false
    tab.startedAt = performance.now()
    if (errorPos(st) >= 0) this.update(tab, { effects: setError.of(null) })
    const verb = analyze ? 'Explain analyze' : 'Explain'
    try {
      const pre = await this.#preflight(tab, script, target, ctrl.signal, analyze)
      if (pre.stop) {
        if (pre.error) tab.plan = { root: null, analyze, statement: '', error: pre.error }
        else if (pre.info) notify.flash(pre.info.replace(/^Run/, verb))
        if (pre.error) tab.view = PLAN
        return
      }
      tab.startedAt = performance.now()
      const res = await this.#call(tab, (tabId) =>
        queryClient().explain(
          { tabId, script, mode: target.mode, start: target.start, end: target.end, params: pre.params, analyze, confirmed: pre.confirmed },
          { signal: ctrl.signal },
        ),
      )
      if (res.tx) tab.tx = { supported: res.tx.supported, autoCommit: res.tx.autoCommit, inTx: res.tx.inTx }
      const statement = res.range ? script.slice(res.range.start, res.range.end) : ''
      if (res.error) {
        tab.plan = { root: null, analyze, statement, error: res.error.message }
        if (res.error.position >= 0 && this.state(tab).doc === doc) this.markError(tab, res.error.position)
      } else {
        tab.plan = { root: res.plan ?? null, analyze, statement, error: '' }
      }
      tab.view = PLAN
    } catch (err) {
      if (ctrl.signal.aborted || isAbort(err)) return
      tab.plan = {
        root: null,
        analyze,
        statement: '',
        error: unimplemented(err) ? `${connections.name(tab.connId)} cannot explain statements` : errorMessage(err),
      }
      tab.view = PLAN
    } finally {
      if (tab.controller === ctrl) tab.controller = null
      tab.busy = ''
      tab.canceling = false
    }
  }

  // #preflight inspects what a run would execute: it refuses writes on a
  // read-only connection, asks for parameters and confirms risky statements.
  // When Inspect fails, the run goes ahead and the server's own checks apply.
  // Without guards (a plain explain) only the parameters are asked for.
  async #preflight(
    tab: Tab,
    script: string,
    target: RunTarget,
    signal: AbortSignal,
    guards = true,
  ): Promise<{ stop: true; error?: string; info: string } | { stop: false; confirmed: boolean; params: Record<string, string> }> {
    let res
    try {
      res = await this.#call(tab, (tabId) => queryClient().inspect({ tabId, script, mode: target.mode, start: target.start, end: target.end }))
    } catch (err) {
      if (signal.aborted || isAbort(err)) return { stop: true, info: 'Run canceled' }
      return { stop: false, confirmed: false, params: {} }
    }
    const name = connections.name(tab.connId)
    const d = decide(res, script, name)
    if (d.kind === 'blocked' && guards) return { stop: true, error: d.message, info: '' }
    let params: Record<string, string> = {}
    if (d.kind !== 'blocked' && d.params.length) {
      const dialect = connections.config(tab.connId)?.kind ?? ''
      const values = await prompts.askParams({
        title: d.params.length === 1 ? 'Parameter value' : 'Parameter values',
        fields: d.params.map((n) => ({ name: n, label: paramLabel(n, dialect) })),
        values: tab.params,
      })
      if (!values || signal.aborted) return { stop: true, info: 'Run canceled: no parameter values' }
      tab.params = { ...tab.params, ...values }
      params = values
    }
    if (d.kind !== 'blocked' && d.confirm && guards) {
      const prod = d.prod
      const ok = await prompts.reviewList({
        title: prod ? `Run on PROD: ${name}` : 'Confirm run',
        text: prod ? `These statements change data on the production connection ${name}.` : 'These statements can change a lot of data.',
        items: d.confirm.map((r) => ({ title: r.verb, detail: r.reason, code: r.text })),
        confirm: 'Run anyway',
        env: connections.env(tab.connId),
      })
      if (!ok || signal.aborted) return { stop: true, info: 'Run not confirmed' }
      return { stop: false, confirmed: true, params }
    }
    return { stop: false, confirmed: false, params }
  }

  // openData loads a table tab's rows (DataService.OpenData); more rows come
  // with fetch. onRows sees every stored batch.
  async openData(
    tab: Tab,
    req: { filter: string; orderBy: string[]; maxRows: number },
    onRows?: (r: ResultSet, rows: readonly Row[], start: number, kept: number) => void,
  ) {
    const t = tab.table
    if (!t) return
    this.#abort(tab)
    const ctrl = new AbortController()
    tab.controller = ctrl
    const model = new RunModel()
    model.onRows = onRows
    tab.run = model
    tab.view = 0
    tab.busy = 'run'
    tab.canceling = false
    tab.startedAt = performance.now()
    tab.touch()
    const node = { path: t.node.path, kind: t.node.kind, name: t.node.name }
    let got = false
    try {
      await this.#call(tab, async (tabId) => {
        const slice = slicer()
        for await (const ev of dataClient().openData({ tabId, node, ...req }, { signal: ctrl.signal })) {
          got = true
          model.apply(ev)
          tab.touch()
          await slice()
        }
      }, () => !got)
    } catch (err) {
      if (!ctrl.signal.aborted && !isAbort(err)) {
        model.messages.push({ kind: 'error', text: errorMessage(err) })
        model.errors++
      }
    } finally {
      this.#end(tab, ctrl, model)
    }
  }

  async fetch(tab: Tab, all: boolean) {
    const model = tab.run
    if (!model.fetchable || tab.busy) return
    const ctrl = new AbortController()
    tab.controller = ctrl
    tab.busy = 'fetch'
    tab.canceling = false
    tab.startedAt = performance.now()
    model.beginFetch()
    tab.touch()
    try {
      await this.#call(tab, async (tabId) => {
        const slice = slicer()
        for await (const ev of queryClient().fetch({ tabId, maxRows: all ? -1 : 0 }, { signal: ctrl.signal })) {
          model.apply(ev)
          tab.touch()
          if (model.truncated && !ctrl.signal.aborted) {
            model.info('Row limit reached; the rest of the result was not loaded')
            ctrl.abort()
          }
          await slice()
        }
      }, () => false)
    } catch (err) {
      if (!ctrl.signal.aborted && !isAbort(err)) {
        model.messages.push({ kind: 'error', text: errorMessage(err) })
        model.errors++
      }
    } finally {
      this.#end(tab, ctrl, model)
    }
  }

  // cancel asks the server to stop the running statement; the stream then
  // ends by itself. If it does not, the stream is closed.
  async cancel(tab: Tab) {
    const ctrl = tab.controller
    if (!ctrl || !tab.serverId) {
      ctrl?.abort()
      return
    }
    tab.canceling = true
    try {
      await queryClient().cancel({ tabId: tab.serverId })
    } catch {
      ctrl.abort()
      return
    }
    setTimeout(() => {
      if (tab.controller === ctrl) ctrl.abort()
    }, 3000)
  }

  async setAutoCommit(tab: Tab, on: boolean) {
    const res = await this.#call(tab, (tabId) => this.#retryBusy(() => queryClient().setAutoCommit({ tabId, on })))
    if (res.tx) tab.tx = { ...res.tx }
  }

  async commit(tab: Tab) {
    const res = await this.#call(tab, (tabId) => queryClient().commit({ tabId }))
    if (res.tx) tab.tx = { ...res.tx }
    notify.flash('Committed')
  }

  async rollback(tab: Tab) {
    const res = await this.#call(tab, (tabId) => queryClient().rollback({ tabId }))
    if (res.tx) tab.tx = { ...res.tx }
    notify.flash('Rolled back')
  }

  // analyze refreshes the statement highlight; the server splits the script
  // only when the document changed.
  async analyze(tab: Tab) {
    const st = this.state(tab)
    const doc = st.doc
    if (tab.analyzed?.doc !== doc) {
      const script = doc.toString()
      let res
      try {
        res = await this.#call(tab, (tabId) => queryClient().analyze({ tabId, script, cursor: st.selection.main.head }))
      } catch {
        return
      }
      tab.analyzed = { doc, ranges: res.statements.map((r) => ({ start: r.start, end: r.end })) }
    }
    this.highlight(tab)
  }

  // highlight marks the statement at the cursor from the last analysis.
  highlight(tab: Tab) {
    const st = this.state(tab)
    const a = tab.analyzed
    if (!a || a.doc !== st.doc) return
    const i = statementAt(a.ranges, st.doc.toString(), st.selection.main.head)
    const r = i >= 0 ? a.ranges[i] : null
    this.update(tab, { effects: setStatement.of(r ? { from: r.start, to: r.end } : null) })
  }

  markError(tab: Tab, pos: number) {
    const len = this.state(tab).doc.length
    const at = Math.min(Math.max(pos, 0), len)
    this.update(tab, {
      effects: [setError.of(at), EditorView.scrollIntoView(at, { y: 'center' })],
      selection: { anchor: at },
    })
    if (editorHost.shown === tab) editorHost.view?.focus()
  }

  #abort(tab: Tab) {
    if (!tab.controller) return
    tab.controller.abort()
    tab.controller = null
    tab.busy = ''
  }

  #end(tab: Tab, ctrl: AbortController, model: RunModel) {
    if (!model.finished) model.finish(Math.round(performance.now() - tab.startedAt), true)
    if (tab.controller === ctrl) {
      tab.controller = null
      tab.busy = ''
      tab.canceling = false
    }
    if (model.tx) tab.tx = { ...model.tx }
    tab.touch()
  }

  // #server returns the tab's server id, opening it when needed.
  #server(tab: Tab): Promise<string> {
    if (tab.serverId) return Promise.resolve(tab.serverId)
    tab.opening ??= queryClient()
      .openTab({ connId: tab.connId, scope: tab.scope })
      .then((res) => {
        tab.serverId = res.tabId
        tab.language = res.language
        return res.tabId
      })
      .finally(() => (tab.opening = null))
    return tab.opening
  }

  // #call runs f with the tab's server id, reopening a tab the server forgot,
  // connecting when the connection is not open and waiting out a busy tab.
  async #call<T>(tab: Tab, f: (tabId: string) => Promise<T>, retryable: () => boolean = () => true): Promise<T> {
    for (let attempt = 0, busy = 0; ; attempt++) {
      const id = await this.#server(tab)
      try {
        return await f(id)
      } catch (err) {
        if (!retryable()) throw err
        if (tabBusy(err) && busy++ < 30) {
          await sleep(50)
          continue
        }
        if (attempt >= 3) throw err
        if (unknownTab(err)) {
          if (tab.serverId === id) tab.serverId = null
          tab.tx = { supported: true, autoCommit: true, inTx: false }
        } else if (notConnected(err)) {
          await connections.ensure(tab.connId)
        } else {
          throw err
        }
      }
    }
  }

  // #retryBusy waits for the server to release a tab whose stream was just closed.
  async #retryBusy<T>(f: () => Promise<T>): Promise<T> {
    for (let i = 0; ; i++) {
      try {
        return await f()
      } catch (err) {
        if (!tabBusy(err) || i >= 20) throw err
        await sleep(50)
      }
    }
  }
}

export const tabs = new Tabs()
