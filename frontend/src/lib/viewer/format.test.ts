import { describe, expect, it } from 'vitest'
import { hexDump, jsonTokens, parseHex, prettyJson } from './format'

describe('prettyJson', () => {
  it('indents without touching numbers or strings', () => {
    expect(prettyJson('{"a":[1,2,{}],"b":{"c":"x, {y}: \\"z\\""},"n":12345678901234567890123,"e":[]}')).toBe(
      '{\n  "a": [\n    1,\n    2,\n    {}\n  ],\n  "b": {\n    "c": "x, {y}: \\"z\\""\n  },\n  "n": 12345678901234567890123,\n  "e": []\n}',
    )
    expect(prettyJson('  42 ')).toBe('42')
    expect(prettyJson('{"a": 1')).toBeNull()
    expect(prettyJson('plain')).toBeNull()
  })

  it('tokenizes for highlighting', () => {
    expect(jsonTokens('{"a": [1, true, "s"]}').map((t) => `${t.kind}:${t.text}`)).toEqual([
      'punct:{',
      'key:"a"',
      'punct::',
      'space: ',
      'punct:[',
      'number:1',
      'punct:,',
      'space: ',
      'literal:true',
      'punct:,',
      'space: ',
      'string:"s"',
      'punct:]',
      'punct:}',
    ])
  })
})

describe('hex', () => {
  it('parses PostgreSQL hex', () => {
    expect(parseHex('\\x00ff10')).toEqual(new Uint8Array([0, 255, 16]))
    expect(parseHex('\\x0')).toBeNull()
    expect(parseHex('abc')).toBeNull()
  })

  it('dumps bytes', () => {
    const b = new TextEncoder().encode('Hello, world!\n\u0000xyz')
    expect(hexDump(b)).toBe(
      '00000000  48 65 6c 6c 6f 2c 20 77  6f 72 6c 64 21 0a 00 78  |Hello, world!..x|\n' +
        '00000010  79 7a                                             |yz|',
    )
    expect(hexDump(new Uint8Array(40), 16).split('\n')).toHaveLength(2)
    expect(hexDump(new Uint8Array(40), 16)).toContain('24 more bytes')
  })
})
