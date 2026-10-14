import { describe, expect, it } from 'vitest'
import { flatten, fmtCount, fmtMs, hottest, misestimate, selfOf, type PlanLike } from './plan'

const node = (op: string, cost: number, actual: number, rows: number, actRows: number, children: PlanLike[] = [], props: Record<string, string> = {}): PlanLike => ({
  op,
  target: '',
  cost,
  rows,
  actual,
  actRows,
  props,
  children,
})

describe('plan', () => {
  const scanA = node('Seq Scan', 40, 30, 1000, 1000)
  const scanB = node('Index Scan', 10, 5, 10, 500)
  const join = node('Hash Join', 100, 50, 10, 500, [scanA, scanB])
  const root = node('Sort', 120, 60, 10, 500, [join])

  it('subtracts children from cumulative figures', () => {
    expect(selfOf(join, true)).toBe(15)
    expect(selfOf(join, false)).toBe(50)
    expect(selfOf(root, true)).toBe(10)
    expect(selfOf(node('x', 0, -1, 1, -1), true)).toBe(0)
  })

  it('flags estimates off by 10x', () => {
    expect(misestimate(scanB)).toBe(50)
    expect(misestimate(scanA)).toBe(0)
    expect(misestimate(node('x', 1, 1, 1000, 5))).toBe(0.005)
    expect(misestimate(node('x', 1, -1, 1, -1))).toBe(0)
    expect(misestimate(node('x', 1, 0, 100, 0, [], { 'Never executed': 'yes' }))).toBe(0)
  })

  it('flattens with shares and collapsing', () => {
    const rows = flatten(root)
    expect(rows.map((r) => [r.id, r.depth])).toEqual([
      ['0', 0],
      ['0.0', 1],
      ['0.0.0', 2],
      ['0.0.1', 2],
    ])
    expect(rows.reduce((s, r) => s + r.share, 0)).toBeCloseTo(1)
    expect(rows[2].share).toBeCloseTo(0.5)
    expect([...hottest(rows, 2)]).toEqual(['0.0.0', '0.0'])
    expect(flatten(root, new Set(['0.0'])).map((r) => r.id)).toEqual(['0', '0.0'])
  })

  it('uses cost when not analyzed', () => {
    const plain = node('Sort', 120, -1, 10, -1, [node('Seq Scan', 40, -1, 1000, -1)])
    const rows = flatten(plain)
    expect(rows[0].self).toBe(80)
    expect(rows[1].share).toBeCloseTo(40 / 120)
  })

  it('formats numbers', () => {
    expect(fmtMs(0.0123)).toBe('0.012 ms')
    expect(fmtMs(5.5)).toBe('5.50 ms')
    expect(fmtMs(1234)).toBe('1.23 s')
    expect(fmtMs(-1)).toBe('')
    expect(fmtCount(12)).toBe('12')
    expect(fmtCount(1500)).toBe('1.5k')
    expect(fmtCount(2_500_000)).toBe('2.5M')
  })
})
