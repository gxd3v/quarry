import { describe, expect, it } from 'vitest'
import { blocks, inline } from './blocks'

describe('release notes markdown', () => {
  it('parses blocks', () => {
    const b = blocks("## What's new\r\n\n- Faster **grid**\n* Fix `COPY` errors\n\nSee [notes](https://example.com/n) for\nmore.\n")
    expect(b.map((x) => x.kind)).toEqual(['heading', 'list', 'para'])
    expect(b[0]).toEqual({ kind: 'heading', level: 2, inline: [{ kind: 'text', text: "What's new" }] })
    expect(b[1].kind === 'list' && b[1].items.length).toBe(2)
    expect(b[2].kind === 'para' && b[2].inline).toEqual([
      { kind: 'text', text: 'See ' },
      { kind: 'link', text: 'notes', url: 'https://example.com/n' },
      { kind: 'text', text: ' for more.' },
    ])
  })

  it('keeps unsafe links as text', () => {
    expect(inline('[x](javascript:alert(1)) <b>')).toEqual([{ kind: 'text', text: '[x](javascript:alert(1)) <b>' }])
    expect(inline('go https://a.b/c now')[1]).toEqual({ kind: 'link', text: 'https://a.b/c', url: 'https://a.b/c' })
  })
})
