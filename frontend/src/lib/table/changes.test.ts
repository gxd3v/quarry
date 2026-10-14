import { describe, expect, it } from 'vitest'
import { ChangeOp } from '../gen/quarry/v1/data_pb'
import { ResultBuffer } from '../grid/buffer'
import { counts, describeCounts, NO_CHANGES, pendingCount, reduce, rowChanges, type Pending, type PendingAction } from './changes'
import { DEFAULT, TableSource } from './source'

const old = (index: number) => ({ kind: 'old' as const, index })
const fresh = (id: number) => ({ kind: 'new' as const, id })
const all = (...actions: PendingAction[]) => actions.reduce(reduce, NO_CHANGES)

describe('pending reducer', () => {
  it('records edits and drops them when set back to the original', () => {
    let p = all({ type: 'set', row: old(2), col: 1, value: 'x', original: 'a' })
    expect(p.edits.get(2)?.get(1)).toBe('x')
    p = reduce(p, { type: 'set', row: old(2), col: 0, value: null, original: '7' })
    expect(pendingCount(p)).toBe(2)
    p = reduce(p, { type: 'set', row: old(2), col: 1, value: 'a', original: 'a' })
    expect([...p.edits.get(2)!]).toEqual([[0, null]])
    p = reduce(p, { type: 'set', row: old(2), col: 0, value: '7', original: '7' })
    expect(p.edits.size).toBe(0)
  })

  it('inserts, edits and deletes new rows', () => {
    let p = all({ type: 'insert' }, { type: 'insert', values: new Map([[0, '5']]) })
    expect(p.inserted.map((r) => r.id)).toEqual([1, 2])
    p = reduce(p, { type: 'set', row: fresh(1), col: 1, value: 'n' })
    expect([...p.inserted[0].values]).toEqual([[1, 'n']])
    p = reduce(p, { type: 'delete', rows: [fresh(2)] })
    expect(p.inserted.map((r) => r.id)).toEqual([1])
    // Ids keep growing after a discard, so stale references never match.
    p = reduce(p, { type: 'discard' })
    expect(pendingCount(p)).toBe(0)
    expect(reduce(p, { type: 'insert' }).inserted[0].id).toBe(3)
  })

  it('deletes loaded rows, ignores edits to them and reverts', () => {
    let p = all({ type: 'set', row: old(0), col: 0, value: 'e' }, { type: 'delete', rows: [old(0), old(3)] })
    expect(counts(p)).toEqual({ updates: 0, cells: 0, inserts: 0, deletes: 2 })
    expect(reduce(p, { type: 'set', row: old(3), col: 0, value: 'z' })).toBe(p)
    p = reduce(p, { type: 'revert', rows: [old(0)] })
    expect([...p.deleted]).toEqual([3])
    expect(p.edits.has(0)).toBe(false)
  })

  it('describes counts by operation', () => {
    const p = all(
      { type: 'set', row: old(0), col: 0, value: 'a' },
      { type: 'set', row: old(0), col: 1, value: 'b' },
      { type: 'set', row: old(1), col: 0, value: 'c' },
      { type: 'insert' },
      { type: 'delete', rows: [old(5)] },
    )
    expect(pendingCount(p)).toBe(5)
    expect(describeCounts(counts(p))).toEqual(['2 rows updated (3 cells)', '1 row inserted', '1 row deleted'])
  })
})

describe('rowChanges', () => {
  const columns = ['id', 'name', 'note']
  const keyRows: Record<number, string> = { 0: '10', 1: '11', 4: '14' }
  const ctx = {
    columns,
    keyCols: [0],
    key: (row: number) => ({ kind: { case: 'text' as const, value: keyRows[row] } }),
  }

  it('orders deletes, updates and inserts with keys from loaded rows', () => {
    const p: Pending = all(
      { type: 'insert', values: new Map([[1, 'new']]) },
      { type: 'set', row: old(1), col: 2, value: null },
      { type: 'set', row: old(1), col: 0, value: '99' },
      { type: 'set', row: old(0), col: 1, value: 'gone anyway' },
      { type: 'delete', rows: [old(4), old(0)] },
    )
    const out = rowChanges(p, ctx)
    expect(out.map((c) => c.op)).toEqual([ChangeOp.DELETE, ChangeOp.DELETE, ChangeOp.UPDATE, ChangeOp.INSERT])
    expect(out[0].key).toEqual({ id: { kind: { case: 'text', value: '10' } } })
    expect(out[1].key).toEqual({ id: { kind: { case: 'text', value: '14' } } })
    // The key is the loaded id even though the edit changes it.
    expect(out[2]).toEqual({
      op: ChangeOp.UPDATE,
      key: { id: { kind: { case: 'text', value: '11' } } },
      values: { id: { kind: { case: 'text', value: '99' } }, note: { kind: { case: 'null', value: true } } },
    })
    expect(out[3]).toEqual({ op: ChangeOp.INSERT, values: { name: { kind: { case: 'text', value: 'new' } } } })
  })

  it('builds fifty updates in one change set', () => {
    let p = NO_CHANGES
    for (let i = 0; i < 50; i++) p = reduce(p, { type: 'set', row: old(i % 25), col: 1 + Math.floor(i / 25), value: `v${i}` })
    const out = rowChanges(p, { ...ctx, key: (row: number) => ({ kind: { case: 'int' as const, value: BigInt(row) } }) })
    expect(pendingCount(p)).toBe(50)
    expect(out).toHaveLength(25)
    expect(out.every((c) => c.op === ChangeOp.UPDATE && Object.keys(c.values!).length === 2)).toBe(true)
  })
})

describe('TableSource', () => {
  const buf = new ResultBuffer([
    { name: 'id', type: 'int4' },
    { name: 'body', type: 'text' },
  ])
  buf.appendCells(['1', 'a'])
  buf.appendCells(['2', 'b'])
  buf.appendCells(['3', 'c'])

  it('overlays edits, appends new rows and maps filtered rows', () => {
    const src = new TableSource(buf)
    src.pending = all(
      { type: 'set', row: old(1), col: 1, value: 'two\nlines' },
      { type: 'insert', values: new Map([[1, 'n']]) },
      { type: 'delete', rows: [old(2)] },
    )
    expect(src.length).toBe(4)
    expect(src.row(1)).toEqual(['2', 'two↵lines'])
    expect(src.value(1, 1)).toBe('two\nlines')
    expect(src.original(1, 1)).toBe('b')
    expect(src.row(3)).toEqual([DEFAULT, 'n'])
    expect(src.value(3, 0)).toBeUndefined()
    expect([src.state(0), src.state(2), src.state(3)]).toEqual(['', 'deleted', 'new'])
    expect([src.edited(1, 1), src.edited(1, 0), src.edited(3, 1)]).toEqual([true, false, true])
    expect(src.slice(1, 3, 1, 1)).toEqual([['two\nlines'], ['c'], ['n']])

    src.index = buf.filter('C')
    expect(src.length).toBe(2)
    expect(src.ref(0)).toEqual(old(2))
    expect(src.viewIndex(old(2))).toBe(0)
    expect(src.viewIndex(old(1))).toBe(-1)
    expect(src.viewIndex(fresh(1))).toBe(1)
  })
})
