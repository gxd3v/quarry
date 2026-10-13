import { describe, expect, it } from 'vitest'
import { afterClose, cycle, DEFAULT_LAYOUT, parseLayout } from './layout'

describe('layout', () => {
  it('parses and clamps stored sizes', () => {
    expect(parseLayout(null)).toEqual(DEFAULT_LAYOUT)
    expect(parseLayout('not json')).toEqual(DEFAULT_LAYOUT)
    expect(parseLayout('null')).toEqual(DEFAULT_LAYOUT)
    expect(parseLayout('{"explorer": 5000, "editor": 0.02}')).toEqual({ explorer: 720, editor: 0.1, scratches: true })
    expect(parseLayout('{"explorer": "x"}')).toEqual(DEFAULT_LAYOUT)
    expect(parseLayout('{"explorer": 300.4, "scratches": false}')).toEqual({
      explorer: 300,
      editor: DEFAULT_LAYOUT.editor,
      scratches: false,
    })
  })

  it('cycles and picks the tab after a close', () => {
    expect(cycle(0, 3, 1)).toBe(1)
    expect(cycle(2, 3, 1)).toBe(0)
    expect(cycle(0, 3, -1)).toBe(2)
    expect(cycle(0, 0, 1)).toBe(-1)
    expect(afterClose(0, 3)).toBe(0)
    expect(afterClose(2, 3)).toBe(1)
    expect(afterClose(0, 1)).toBe(-1)
  })
})
