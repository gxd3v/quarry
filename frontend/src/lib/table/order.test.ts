import { describe, expect, it } from 'vitest'
import { cycleOrder, orderBy, parseOrder, sortOf, type OrderTerm } from './order'

describe('cycleOrder', () => {
  it('cycles a column asc → desc → none on plain clicks', () => {
    let o: OrderTerm[] = []
    o = cycleOrder(o, 'id', false)
    expect(o).toEqual([{ column: 'id', dir: 'asc' }])
    o = cycleOrder(o, 'id', false)
    expect(o).toEqual([{ column: 'id', dir: 'desc' }])
    o = cycleOrder(o, 'id', false)
    expect(o).toEqual([])
  })

  it('replaces other terms on a plain click', () => {
    const o: OrderTerm[] = [
      { column: 'a', dir: 'asc' },
      { column: 'b', dir: 'desc' },
    ]
    expect(cycleOrder(o, 'c', false)).toEqual([{ column: 'c', dir: 'asc' }])
    expect(cycleOrder(o, 'a', false)).toEqual([{ column: 'a', dir: 'desc' }])
    expect(cycleOrder(o, 'b', false)).toEqual([])
  })

  it('adds and cycles secondary terms in place with shift', () => {
    let o: OrderTerm[] = [{ column: 'a', dir: 'asc' }]
    o = cycleOrder(o, 'b', true)
    expect(o).toEqual([
      { column: 'a', dir: 'asc' },
      { column: 'b', dir: 'asc' },
    ])
    o = cycleOrder(o, 'a', true)
    expect(o).toEqual([
      { column: 'a', dir: 'desc' },
      { column: 'b', dir: 'asc' },
    ])
    o = cycleOrder(o, 'a', true)
    expect(o).toEqual([{ column: 'b', dir: 'asc' }])
    expect(sortOf(o, 'b')).toEqual({ dir: 'asc', rank: 1 })
    expect(sortOf(o, 'a')).toBeNull()
  })
})

describe('orderBy', () => {
  it('renders server terms, quoting names that need it', () => {
    expect(
      orderBy([
        { column: 'id', dir: 'asc' },
        { column: 'Name', dir: 'desc' },
        { column: 'my col', dir: 'asc' },
        { column: 'say "hi"', dir: 'desc' },
      ]),
    ).toEqual(['id', 'Name DESC', '"my col"', '"say ""hi""" DESC'])
  })

  it('parses stored terms defensively', () => {
    expect(parseOrder([{ column: 'a', dir: 'desc' }, { column: '', dir: 'asc' }, { column: 'b', dir: 'up' }, 3, null])).toEqual([
      { column: 'a', dir: 'desc' },
    ])
    expect(parseOrder('x')).toEqual([])
  })
})
