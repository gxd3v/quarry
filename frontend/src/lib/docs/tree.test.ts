import { describe, expect, it } from 'vitest'
import { idOf, key, parseDoc, summary, toTree } from './tree'

describe('document trees', () => {
  it('renders Extended JSON as mongosh values', () => {
    const d = parseDoc(
      JSON.stringify({
        _id: { $oid: '65a1b2c3d4e5f6a7b8c9d0e1' },
        at: { $date: '2024-01-02T03:04:05Z' },
        old: { $date: { $numberLong: '0' } },
        price: { $numberDecimal: '1.10' },
        n: { $numberLong: '9007199254740993' },
        bin: { $binary: { base64: 'AQI=', subType: '00' } },
        re: { $regularExpression: { pattern: 'a+', options: 'i' } },
        tags: ['a', 1, true, null],
        'odd key': { nested: { deep: 1 } },
      }),
    )
    expect(d?.kind).toBe('object')
    expect(idOf(d)).toBe('ObjectId("65a1b2c3d4e5f6a7b8c9d0e1")')
    const s = summary(d!, 1000)
    expect(s).toContain('at: ISODate("2024-01-02T03:04:05Z")')
    expect(s).toContain('old: ISODate("1970-01-01T00:00:00.000Z")')
    expect(s).toContain('price: NumberDecimal("1.10")')
    expect(s).toContain('n: NumberLong("9007199254740993")')
    expect(s).toContain('bin: BinData(0, "AQI=")')
    expect(s).toContain('re: /a+/i')
    expect(s).toContain('tags: ["a", 1, true, null]')
    expect(s).toContain('"odd key": { nested: { deep: 1 } }')
  })

  it('cuts long summaries', () => {
    const s = summary(toTree({ a: 'x'.repeat(200) }), 40)
    expect(s.length).toBe(40)
    expect(s.endsWith('…')).toBe(true)
    expect(summary(toTree({}))).toBe('{}')
  })

  it('handles bad input and keys', () => {
    expect(parseDoc('not json')).toBeNull()
    expect(parseDoc('')).toBeNull()
    expect(key('a_b$1')).toBe('a_b$1')
    expect(key('a b')).toBe('"a b"')
    expect(idOf(toTree([1]))).toBe('')
  })
})
