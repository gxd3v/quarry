import { describe, expect, it } from 'vitest'
import { keepMapping, mappingProblem, nullText } from './csv'

describe('csv import form', () => {
  it('keeps the mapping for the same columns', () => {
    expect(keepMapping(['a', 'b'], ['x', ''], ['a', 'b'], ['a', 'b'])).toEqual(['x', ''])
    expect(keepMapping(['a', 'b'], ['x', ''], ['a', 'c'], ['a'])).toEqual(['a', ''])
    expect(keepMapping([], [], ['a'], [])).toEqual([''])
  })

  it('checks mappings', () => {
    expect(mappingProblem(['', ''])).toMatch(/at least one/)
    expect(mappingProblem(['id', 'id'])).toMatch(/id is mapped from more than one/)
    expect(mappingProblem(['id', '', 'name'])).toBe('')
  })

  it('picks the NULL marker', () => {
    expect(nullText('empty', 'x')).toBeUndefined()
    expect(nullText('text', 'NULL')).toBe('NULL')
    expect(nullText('none', '')).toBe('\u0000')
  })
})
