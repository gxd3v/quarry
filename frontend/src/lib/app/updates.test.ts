import { describe, expect, it } from 'vitest'
import { due } from './updates'

describe('update checks', () => {
  it('runs once a day', () => {
    const now = 10 * 24 * 3600 * 1000
    expect(due(null, now)).toBe(true)
    expect(due(String(now - 1000), now)).toBe(false)
    expect(due(String(now - 25 * 3600 * 1000), now)).toBe(true)
    expect(due('garbage', now)).toBe(true)
    expect(due(String(now + 5000), now)).toBe(true)
  })
})
