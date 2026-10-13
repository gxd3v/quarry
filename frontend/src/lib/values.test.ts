import { create } from '@bufbuild/protobuf'
import { TimestampSchema } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'
import { ValueListSchema, ValueSchema, type Value } from './gen/quarry/v1/common_pb'
import { compactJson, formatValue, isNumericType, MAX_BYTES_SHOWN, MAX_CELL_CHARS } from './values'

const v = (kind: Value['kind']): Value => create(ValueSchema, { kind })
const text = (s: string) => v({ case: 'text', value: s })
const arr = (...values: Value[]) => v({ case: 'array', value: create(ValueListSchema, { values }) })

describe('formatValue', () => {
  it('returns null for SQL NULL and unset values', () => {
    expect(formatValue(v({ case: 'null', value: true }))).toBeNull()
    expect(formatValue(create(ValueSchema))).toBeNull()
    expect(formatValue(undefined)).toBeNull()
  })

  it('formats scalars', () => {
    expect(formatValue(v({ case: 'bool', value: true }))).toBe('true')
    expect(formatValue(v({ case: 'int', value: 9007199254740993n }))).toBe('9007199254740993')
    expect(formatValue(v({ case: 'int', value: -5n }))).toBe('-5')
    expect(formatValue(v({ case: 'float', value: 1.5 }))).toBe('1.5')
    expect(formatValue(v({ case: 'float', value: Number.NaN }))).toBe('NaN')
    expect(formatValue(v({ case: 'number', value: '12345678901234567890.000100' }))).toBe(
      '12345678901234567890.000100',
    )
    expect(formatValue(text(''))).toBe('')
  })

  it('keeps text on one line without control characters', () => {
    expect(formatValue(text('a\r\nb\nc\td\u0001'))).toBe('a↵b↵c d�')
  })

  it('clips long text', () => {
    const s = formatValue(text('x'.repeat(MAX_CELL_CHARS * 3)))!
    expect(s).toHaveLength(MAX_CELL_CHARS)
    expect(s.endsWith('…')).toBe(true)
  })

  it('formats bytes as truncated hex', () => {
    expect(formatValue(v({ case: 'bytes', value: new Uint8Array([0, 15, 255]) }))).toBe('\\x000fff')
    const long = formatValue(v({ case: 'bytes', value: new Uint8Array(100).fill(171) }))!
    expect(long).toBe('\\x' + 'ab'.repeat(MAX_BYTES_SHOWN) + '…')
  })

  it('formats timestamps in local time', () => {
    const d = new Date(2026, 9, 13, 9, 5, 7)
    const seconds = BigInt(d.getTime() / 1000)
    const ts = (nanos: number) => v({ case: 'time', value: create(TimestampSchema, { seconds, nanos }) })
    expect(formatValue(ts(0))).toBe('2026-10-13 09:05:07')
    expect(formatValue(ts(123_000_000))).toBe('2026-10-13 09:05:07.123')
    expect(formatValue(ts(123_456_000))).toBe('2026-10-13 09:05:07.123456')
  })

  it('compacts json without losing big numbers', () => {
    const json = '{\n  "id": 12345678901234567890,\n  "s": "a  b",\n  "q": "x\\" y"\n}'
    expect(formatValue(v({ case: 'json', value: json }))).toBe('{"id":12345678901234567890,"s":"a  b","q":"x\\" y"}')
  })

  it('formats arrays in PostgreSQL style', () => {
    const int = (n: bigint) => v({ case: 'int', value: n })
    expect(formatValue(arr(int(1n), int(2n), v({ case: 'null', value: true })))).toBe('{1,2,NULL}')
    expect(formatValue(arr(text('a'), text('b c'), text(''), text('null'), text('q"\\')))).toBe(
      '{a,"b c","","null","q\\"\\\\"}',
    )
    expect(formatValue(arr(arr(int(1n), int(2n)), arr(int(3n), int(4n))))).toBe('{{1,2},{3,4}}')
    expect(formatValue(arr())).toBe('{}')
  })
})

describe('compactJson', () => {
  it('stops early past the limit', () => {
    const s = compactJson('[' + ' 1,'.repeat(1000) + ' 1]', 10)
    expect(s.length).toBeLessThan(20)
  })
})

describe('isNumericType', () => {
  it.each([
    ['int4', true],
    ['bigint', true],
    ['numeric(12,2)', true],
    ['double precision', true],
    ['NUMBER', true],
    ['float8', true],
    ['text', false],
    ['interval', false],
    ['timestamptz', false],
    ['point', false],
  ])('%s → %s', (type, want) => {
    expect(isNumericType(type)).toBe(want)
  })
})
