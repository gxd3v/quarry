import { describe, expect, it } from 'vitest'
import { fuzzy, rank, segments } from './fuzzy'

describe('fuzzy', () => {
  it('matches subsequences case-insensitively', () => {
    expect(fuzzy('ord', 'public.Orders')).not.toBeNull()
    expect(fuzzy('pbo', 'public.orders')?.positions).toEqual([0, 2, 7])
    expect(fuzzy('xyz', 'public.orders')).toBeNull()
    expect(fuzzy('ordersx', 'orders')).toBeNull()
    expect(fuzzy('', 'anything')).toEqual({ score: 0, positions: [] })
    expect(fuzzy('r s', 'run script')?.positions).toEqual([0, 4])
  })

  it('prefers word starts and runs', () => {
    const s = (q: string, t: string) => fuzzy(q, t)!.score
    expect(s('ord', 'orders')).toBeGreaterThan(s('ord', 'coordinates'))
    expect(s('rs', 'Run script')).toBeGreaterThan(s('rs', 'Rollback transaction cursors'))
    expect(s('ord', 'public.orders')).toBeGreaterThan(s('ord', 'public.word_index'))
    expect(s('ci', 'customerId')).toBeGreaterThan(s('ci', 'cascading'))
    // Shorter wins a tie.
    expect(s('ord', 'orders')).toBeGreaterThan(s('ord', 'orders_archive'))
  })

  it('finds the best alignment, not the first', () => {
    // Greedy would take the "o" of "foo"; the best match is the word "orders".
    expect(fuzzy('ord', 'foo_orders')?.positions).toEqual([4, 5, 6])
  })
})

describe('rank', () => {
  const items = ['tab.close', 'run.script', 'run.statement', 'focus.results', 'file.save']
  it('filters and sorts by score, keeping order otherwise', () => {
    expect(rank(items, '', (s) => s).map((r) => r.item)).toEqual(items)
    expect(rank(items, 'run', (s) => s).map((r) => r.item)).toEqual(['run.script', 'run.statement'])
    expect(rank(items, 'rst', (s) => s)[0].item).toBe('run.statement')
    expect(rank(items, 'zzz', (s) => s)).toEqual([])
    expect(rank(items, 's', (s) => s, 2)).toHaveLength(2)
  })
})

describe('segments', () => {
  it('splits text into hit runs', () => {
    expect(segments('orders', [0, 1, 3])).toEqual([
      { text: 'or', hit: true },
      { text: 'd', hit: false },
      { text: 'e', hit: true },
      { text: 'rs', hit: false },
    ])
    expect(segments('ab', [])).toEqual([{ text: 'ab', hit: false }])
  })
})
