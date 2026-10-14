import { describe, expect, it } from 'vitest'
import { isTheme, themeVars, THEMES } from './themes'

describe('themes', () => {
  it('has full palettes', () => {
    expect(themeVars('mocha')).toEqual({})
    for (const t of THEMES.filter((t) => t !== 'mocha')) {
      const v = themeVars(t)
      expect(Object.keys(v)).toHaveLength(26)
      for (const c of Object.values(v)) expect(c).toMatch(/^#[0-9a-f]{6}$/)
    }
    expect(themeVars('latte')['--base']).toBe('#eff1f5')
    expect(themeVars('frappe')['--crust']).toBe('#232634')
  })

  it('checks names', () => {
    expect(isTheme('latte')).toBe(true)
    expect(isTheme('solarized')).toBe(false)
    expect(isTheme(1)).toBe(false)
  })
})
