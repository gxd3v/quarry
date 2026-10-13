import { describe, expect, it } from 'vitest'
import { macrotask, slicer } from './yield'

describe('slicer', () => {
  it('stays in microtasks within the budget and yields after it', async () => {
    const slice = slicer(5)
    let timer = false
    setTimeout(() => (timer = true), 0)
    await slice()
    expect(timer).toBe(false)
    const t = performance.now()
    while (performance.now() - t < 10) {
      // Spend the budget.
    }
    await slice()
    await new Promise((r) => setTimeout(r, 1))
    expect(timer).toBe(true)
  })

  it('resolves macrotasks in order', async () => {
    const out: number[] = []
    await Promise.all([1, 2, 3].map((n) => macrotask().then(() => out.push(n))))
    expect(out).toEqual([1, 2, 3])
  })
})
