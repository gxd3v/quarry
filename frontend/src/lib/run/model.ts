import type { Column } from '../gen/quarry/v1/common_pb'
import type { RunEvent, StatementDone, TxState } from '../gen/quarry/v1/query_pb'
import { ResultBuffer } from '../grid/buffer'

// Results keep at most this many rows; Load all stops there.
export const MAX_RESULT_ROWS = 2_000_000

export interface ResultSet {
  statement: number
  columns: Column[]
  buffer: ResultBuffer
  // Set once the statement (or the last fetch) completed.
  rows: number
  hasMore: boolean
  elapsedMs: number
  tag: string
}

export type MessageKind = 'summary' | 'notice' | 'error' | 'info'

export interface RunMessage {
  kind: MessageKind
  text: string
  severity?: string
  code?: string
  detail?: string
  hint?: string
  // Absolute offset in the script, -1 when unknown.
  position?: number
}

export interface TxInfo {
  supported: boolean
  autoCommit: boolean
  inTx: boolean
}

// RunModel folds the events of one Run, and of the Fetches that continue it,
// into results and messages. It is plain (not reactive): owners bump a
// version after applying events.
export class RunModel {
  results: ResultSet[] = []
  messages: RunMessage[] = []
  statements = 0
  statement = -1
  range: { start: number; end: number } | null = null
  // The first error that has a position in the script.
  errorAt = -1
  errors = 0
  elapsedMs = 0
  canceled = false
  finished = false
  tx: TxInfo | null = null
  #open: ResultSet | null = null
  #fetching = false
  readonly #limit: number

  constructor(limit = MAX_RESULT_ROWS) {
    this.#limit = limit
  }

  // The result still open for Fetch, if any.
  get fetchable(): ResultSet | null {
    const last = this.results.at(-1)
    return this.finished && last?.hasMore ? last : null
  }

  get truncated(): boolean {
    return this.results.some((r) => r.buffer.truncated)
  }

  // beginFetch prepares for the events of a Fetch on the last result.
  beginFetch() {
    this.#open = this.fetchable
    this.#fetching = true
    this.finished = false
    this.canceled = false
  }

  apply(ev: RunEvent) {
    const e = ev.event
    switch (e.case) {
      case 'started':
        this.statements = e.value.statements
        if (e.value.statements === 0) this.info('Nothing to run at the cursor')
        break
      case 'statement':
        this.statement = e.value.index
        this.range = e.value.range ? { start: e.value.range.start, end: e.value.range.end } : null
        this.#open = null
        break
      case 'columns': {
        const r: ResultSet = {
          statement: this.statement,
          columns: e.value.columns,
          buffer: new ResultBuffer(e.value.columns, this.#limit),
          rows: 0,
          hasMore: false,
          elapsedMs: 0,
          tag: '',
        }
        this.results.push(r)
        this.#open = r
        break
      }
      case 'rows':
        this.#open?.buffer.appendRows(e.value.rows)
        break
      case 'done':
        this.#done(e.value)
        break
      case 'error': {
        const q = e.value
        this.errors++
        if (q.position >= 0 && this.errorAt < 0) this.errorAt = q.position
        this.messages.push({
          kind: 'error',
          text: q.message,
          code: q.code || undefined,
          detail: q.detail || undefined,
          hint: q.hint || undefined,
          position: q.position,
        })
        if (this.#open) this.#open.hasMore = false
        break
      }
      case 'message':
        this.messages.push({ kind: 'notice', severity: e.value.severity, text: e.value.text })
        break
      case 'finished':
        this.finish(Number(e.value.elapsedMs), e.value.canceled, e.value.tx)
        break
    }
  }

  // finish records the end of a run or fetch; streams that break off call it
  // with canceled set.
  finish(elapsedMs: number, canceled: boolean, tx?: TxState) {
    if (this.finished) return
    this.finished = true
    this.elapsedMs = elapsedMs
    this.canceled = canceled
    if (tx) this.tx = { supported: tx.supported, autoCommit: tx.autoCommit, inTx: tx.inTx }
    if (canceled) {
      if (this.#open) this.#open.hasMore = false
      this.info(`Canceled after ${elapsedMs} ms`)
    }
    this.#open = null
    this.#fetching = false
  }

  info(text: string) {
    this.messages.push({ kind: 'info', text })
  }

  #done(d: StatementDone) {
    const ms = Number(d.elapsedMs)
    const rows = Number(d.rows)
    const r = this.#open
    const fetched = this.#fetching && r ? rows - r.rows : -1
    if (r) {
      r.rows = rows
      r.hasMore = d.hasMore && !r.buffer.truncated
      r.elapsedMs = this.#fetching ? r.elapsedMs + ms : ms
      r.tag = d.tag
    }
    this.messages.push({ kind: 'summary', text: summary(d, r !== null, fetched) })
  }
}

// summary describes a finished statement, e.g. "SELECT 10000 rows in 35 ms";
// fetched is the number of rows a Fetch added, -1 for a Run.
export function summary(d: StatementDone, hasResult: boolean, fetched = -1): string {
  const ms = Number(d.elapsedMs)
  const rows = Number(d.rows)
  const more = d.hasMore ? ' (more available)' : ''
  if (fetched >= 0) return `Fetched ${fetched.toLocaleString('en-US')} more (${count(rows)} total)${more} in ${ms} ms`
  if (hasResult) {
    const verb = d.tag.split(' ')[0] || 'SELECT'
    return `${verb} ${count(rows)}${more} in ${ms} ms`
  }
  if (d.tag) return `${d.tag} in ${ms} ms`
  const affected = Number(d.affected)
  return affected >= 0 ? `${affected} rows affected in ${ms} ms` : `OK in ${ms} ms`
}

function count(n: number): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? 'row' : 'rows'}`
}
