import { describe, expect, it } from 'vitest'
import { statementAt } from './statements'

describe('statementAt', () => {
  const text = 'select 1;\n\nselect 2; select 3;\n'
  const ranges = [
    { start: 0, end: 8 },
    { start: 11, end: 19 },
    { start: 21, end: 29 },
  ]

  it('finds the statement around the cursor', () => {
    expect(statementAt([], text, 0)).toBe(-1)
    expect(statementAt(ranges, text, 0)).toBe(0)
    expect(statementAt(ranges, text, 8)).toBe(0)
    expect(statementAt(ranges, text, 15)).toBe(1)
    expect(statementAt(ranges, text, 25)).toBe(2)
  })

  it('prefers the previous statement on its own line', () => {
    expect(statementAt(ranges, text, 9)).toBe(0)
    expect(statementAt(ranges, text, 10)).toBe(0)
    expect(statementAt(ranges, text, 20)).toBe(1)
  })

  it('takes the next statement from a blank line before it', () => {
    expect(statementAt(ranges, 'select 1;\n\n  select 2;', 11)).toBe(1)
  })

  it('falls back to the last statement', () => {
    expect(statementAt(ranges, text, 30)).toBe(2)
  })
})
