import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { RowSchema, ValueSchema, type Value } from '../gen/quarry/v1/common_pb'
import { ResultBuffer } from './buffer'

const cols = [
  { name: 'id', type: 'int8' },
  { name: 'name', type: 'text' },
  { name: 'note', type: 'text' },
]
const text = (s: string): Value => create(ValueSchema, { kind: { case: 'text', value: s } })
const int = (n: bigint): Value => create(ValueSchema, { kind: { case: 'int', value: n } })
const nul = (): Value => create(ValueSchema, { kind: { case: 'null', value: true } })

describe('ResultBuffer', () => {
  it('derives column metadata', () => {
    const b = new ResultBuffer(cols)
    expect(b.columns.map((c) => c.numeric)).toEqual([true, false, false])
    expect(b.length).toBe(0)
    expect(b.row(0)).toEqual([])
  })

  it('appends proto rows and reads display cells back', () => {
    const b = new ResultBuffer(cols)
    const n = b.appendRows([
      create(RowSchema, { values: [int(1n), text('alice'), nul()] }),
      create(RowSchema, { values: [int(2n), text(''), text('multi\nline')] }),
      create(RowSchema, { values: [int(3n)] }),
    ])
    expect(n).toBe(3)
    expect(b.length).toBe(3)
    expect(b.row(0)).toEqual(['1', 'alice', null])
    expect(b.row(1)).toEqual(['2', '', 'multi↵line'])
    expect(b.row(2)).toEqual(['3', null, null])
    expect(b.row(3)).toEqual([])
  })

  it('keeps empty strings distinct from NULL', () => {
    const b = new ResultBuffer(cols)
    b.appendCells(['', null, ''])
    b.appendCells([null, null, null])
    expect(b.row(0)).toEqual(['', null, ''])
    expect(b.row(1)).toEqual([null, null, null])
  })

  it('handles a single column and zero columns', () => {
    const one = new ResultBuffer([{ name: 'x', type: 'text' }])
    one.appendCells([''])
    one.appendCells([null])
    expect(one.row(0)).toEqual([''])
    expect(one.row(1)).toEqual([null])

    const none = new ResultBuffer([])
    none.appendRows([create(RowSchema)])
    expect(none.length).toBe(1)
    expect(none.row(0)).toEqual([])
  })

  it('stops at the limit and flags truncation', () => {
    const b = new ResultBuffer(cols, 2)
    const row = create(RowSchema, { values: [int(1n), text('a'), text('b')] })
    expect(b.appendRows([row, row, row])).toBe(2)
    expect(b.length).toBe(2)
    expect(b.truncated).toBe(true)
    expect(b.appendCells(['1', '2', '3'])).toBe(false)
  })

  it('appends many rows cheaply', () => {
    const b = new ResultBuffer(cols)
    const batch = Array.from({ length: 2000 }, (_, i) =>
      create(RowSchema, { values: [int(BigInt(i)), text(`name ${i}`), i % 3 ? nul() : text('x')] }),
    )
    for (let i = 0; i < 50; i++) b.appendRows(batch)
    expect(b.length).toBe(100_000)
    expect(b.row(99_999)).toEqual(['1999', 'name 1999', null])
  })
})

describe('ResultBuffer full values', () => {
  const bytes = (b: Uint8Array): Value => create(ValueSchema, { kind: { case: 'bytes', value: b } })
  const json = (s: string): Value => create(ValueSchema, { kind: { case: 'json', value: s } })

  it('keeps the full value of lossy cells only', () => {
    const b = new ResultBuffer(cols)
    const long = 'x'.repeat(2000)
    b.appendRows([
      create(RowSchema, { values: [int(1n), text(long), text('a\nb')] }),
      create(RowSchema, { values: [int(2n), text('short'), nul()] }),
    ])
    expect(b.row(0)[1]!.length).toBe(500)
    expect(b.full(0, 1)).toBe(long)
    expect(b.full(0, 2)).toBe('a\nb')
    expect(b.full(0, 0)).toBeUndefined()
    expect(b.full(1, 1)).toBeUndefined()
    expect(b.fullCount).toBe(2)
    expect(b.text(0, 1)).toBe(long)
    expect(b.text(1, 1)).toBe('short')
    expect(b.text(1, 2)).toBeNull()
    expect(b.slice(0, 5, 1, 2)).toEqual([
      [long, 'a\nb'],
      ['short', null],
    ])
  })

  it('keeps long bytes and long JSON documents', () => {
    const b = new ResultBuffer([
      { name: 'b', type: 'bytea' },
      { name: 'j', type: 'jsonb' },
    ])
    const big = new Uint8Array(100).map((_, i) => i)
    const doc = JSON.stringify({ items: Array.from({ length: 200 }, (_, i) => ({ id: i })) }, null, 2)
    b.appendRows([
      create(RowSchema, { values: [bytes(big), json(doc)] }),
      create(RowSchema, { values: [bytes(new Uint8Array([1, 2])), json('{"a": 1}')] }),
    ])
    expect(b.full(0, 0)).toEqual(big)
    expect(b.text(0, 0)).toBe('\\x' +Array.from(big, (x) => x.toString(16).padStart(2, '0')).join(''))
    expect(b.full(0, 1)).toBe(doc)
    expect(b.full(1, 0)).toBeUndefined()
    expect(b.full(1, 1)).toBeUndefined()
    expect(b.text(1, 1)).toBe('{"a":1}')
  })

  it('learns column kinds from the first non-NULL value', () => {
    const b = new ResultBuffer(cols)
    b.appendRows([create(RowSchema, { values: [nul(), text('a'), nul()] })])
    expect(b.columns.map((c) => c.kind)).toEqual([undefined, 'text', undefined])
    b.appendRows([create(RowSchema, { values: [int(1n), nul(), nul()] })])
    expect(b.columns.map((c) => c.kind)).toEqual(['int', 'text', undefined])
  })
})
