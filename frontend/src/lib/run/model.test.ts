import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { RowSchema, ValueSchema } from '../gen/quarry/v1/common_pb'
import { RunEventSchema, type RunEvent } from '../gen/quarry/v1/query_pb'
import { RunModel, summary } from './model'

type Ev = RunEvent['event']

const ev = (event: Ev) => create(RunEventSchema, { event })
const rows = (...ns: number[]) =>
  ns.map((n) => create(RowSchema, { values: [create(ValueSchema, { kind: { case: 'int', value: BigInt(n) } })] }))
const done = (rows: number, hasMore = false, tag = '', affected = -1) =>
  ev({ case: 'done', value: { rows: BigInt(rows), hasMore, tag, affected: BigInt(affected), elapsedMs: 5n } as never })
const finished = (canceled = false) =>
  ev({ case: 'finished', value: { elapsedMs: 9n, canceled, tx: { supported: true, autoCommit: true, inTx: false } } as never })

function feed(m: RunModel, events: RunEvent[]) {
  for (const e of events) m.apply(e)
}

describe('RunModel', () => {
  it('collects results, summaries and the open result', () => {
    const m = new RunModel()
    feed(m, [
      ev({ case: 'started', value: { runId: 'r', statements: 2 } as never }),
      ev({ case: 'statement', value: { index: 0, range: { start: 0, end: 8 } } as never }),
      ev({ case: 'done', value: { rows: 0n, hasMore: false, tag: 'UPDATE 3', affected: 3n, elapsedMs: 2n } as never }),
      ev({ case: 'statement', value: { index: 1, range: { start: 9, end: 20 } } as never }),
      ev({ case: 'columns', value: { columns: [{ name: 'n', type: 'int4' }] } as never }),
      ev({ case: 'rows', value: { rows: rows(1, 2) } as never }),
      ev({ case: 'rows', value: { rows: rows(3) } as never }),
      done(3, true, 'SELECT 3'),
      finished(),
    ])
    expect(m.results).toHaveLength(1)
    expect(m.results[0].buffer.length).toBe(3)
    expect(m.results[0].statement).toBe(1)
    expect(m.messages.map((x) => x.text)).toEqual(['UPDATE 3 in 2 ms', 'SELECT 3 rows (more available) in 5 ms'])
    expect(m.fetchable).toBe(m.results[0])
    expect(m.tx).toEqual({ supported: true, autoCommit: true, inTx: false })

    m.beginFetch()
    feed(m, [ev({ case: 'rows', value: { rows: rows(4, 5) } as never }), done(5), finished()])
    expect(m.results[0].buffer.length).toBe(5)
    expect(m.results[0].hasMore).toBe(false)
    expect(m.fetchable).toBeNull()
    expect(m.messages.at(-1)?.text).toBe('Fetched 2 more (5 rows total) in 5 ms')
  })

  it('records errors with their position', () => {
    const m = new RunModel()
    feed(m, [
      ev({ case: 'statement', value: { index: 0 } as never }),
      ev({ case: 'message', value: { severity: 'NOTICE', text: 'hello' } as never }),
      ev({ case: 'error', value: { message: 'syntax error', code: '42601', detail: '', hint: 'h', position: 7 } as never }),
      finished(),
    ])
    expect(m.errorAt).toBe(7)
    expect(m.errors).toBe(1)
    expect(m.messages[0]).toEqual({ kind: 'notice', severity: 'NOTICE', text: 'hello' })
    expect(m.messages[1]).toMatchObject({ kind: 'error', code: '42601', hint: 'h', detail: undefined, position: 7 })
  })

  it('closes the open result on cancel and stops at the row limit', () => {
    const m = new RunModel(2)
    feed(m, [
      ev({ case: 'columns', value: { columns: [{ name: 'n', type: 'int4' }] } as never }),
      ev({ case: 'rows', value: { rows: rows(1, 2, 3) } as never }),
    ])
    m.finish(12, true)
    expect(m.results[0].buffer.length).toBe(2)
    expect(m.truncated).toBe(true)
    expect(m.canceled).toBe(true)
    expect(m.fetchable).toBeNull()
    expect(m.messages.at(-1)?.text).toBe('Canceled after 12 ms')
    m.finish(20, false)
    expect(m.elapsedMs).toBe(12)
  })

  it('summarizes statements without results', () => {
    const d = (tag: string, affected: number) =>
      ({ tag, affected: BigInt(affected), rows: 0n, elapsedMs: 1n, hasMore: false }) as never
    expect(summary(d('', 4), false)).toBe('4 rows affected in 1 ms')
    expect(summary(d('', -1), false)).toBe('OK in 1 ms')
    expect(summary(d('CREATE TABLE', -1), false)).toBe('CREATE TABLE in 1 ms')
  })
})
