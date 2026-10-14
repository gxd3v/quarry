import { describe, expect, it } from 'vitest'
import { themeFromCSS } from './session.svelte'

describe('terminal theme', () => {
  it('maps the app palette', () => {
    const vars: Record<string, string> = { '--crust': ' #111 ', '--text': '#ccc', '--red': '#f00', '--surface1': '#444' }
    const t = themeFromCSS((n) => vars[n] ?? '')
    expect(t.background).toBe('#111')
    expect(t.foreground).toBe('#ccc')
    expect(t.red).toBe('#f00')
    expect(t.black).toBe('#444')
  })
})
