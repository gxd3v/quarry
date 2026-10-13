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
