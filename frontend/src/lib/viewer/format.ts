// Formatting for the cell viewer. JSON is re-indented by scanning its text,
// never by parsing it into numbers, so big integers keep their digits.

// prettyJson indents a JSON document, or returns null when it is not JSON.
export function prettyJson(s: string, indent = '  '): string | null {
  try {
    JSON.parse(s)
  } catch {
    return null
  }
  const out: string[] = []
  let depth = 0
  const nl = () => out.push('\n' + indent.repeat(depth))
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '"') {
      let j = i + 1
      while (j < s.length && s[j] !== '"') j += s[j] === '\\' ? 2 : 1
      out.push(s.slice(i, j + 1))
      i = j
    } else if (c === '{' || c === '[') {
      const close = c === '{' ? '}' : ']'
      let j = i + 1
      while (j < s.length && /\s/.test(s[j])) j++
      if (s[j] === close) {
        out.push(c + close)
        i = j
      } else {
        out.push(c)
        depth++
        nl()
      }
    } else if (c === '}' || c === ']') {
      depth--
      nl()
      out.push(c)
    } else if (c === ',') {
      out.push(',')
      nl()
    } else if (c === ':') {
      out.push(': ')
    } else if (!/\s/.test(c)) {
      out.push(c)
    }
  }
  return out.join('')
}

export type JsonTokenKind = 'key' | 'string' | 'number' | 'literal' | 'punct' | 'space'

// jsonTokens splits (pretty) JSON text into tokens for highlighting.
export function jsonTokens(s: string): { text: string; kind: JsonTokenKind }[] {
  const out: { text: string; kind: JsonTokenKind }[] = []
  const re = /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d[\d.eE+-]*)|(true|false|null)|([{}[\],:])|(\s+)|(.)/gy
  let m: RegExpExecArray | null
  while ((m = re.exec(s))) {
    if (m[1]) {
      out.push({ text: m[1], kind: m[2] ? 'key' : 'string' })
      if (m[2]) out.push({ text: m[2], kind: 'punct' })
    } else if (m[3]) out.push({ text: m[3], kind: 'number' })
    else if (m[4]) out.push({ text: m[4], kind: 'literal' })
    else if (m[5]) out.push({ text: m[5], kind: 'punct' })
    else out.push({ text: m[0], kind: 'space' })
  }
  return out
}

// parseHex reads PostgreSQL's \x hex format, or returns null.
export function parseHex(s: string): Uint8Array | null {
  if (!s.startsWith('\\x') || s.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(s.slice(2))) return null
  const out = new Uint8Array((s.length - 2) / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(2 + i * 2, 4 + i * 2), 16)
  return out
}

// hexDump renders bytes as offset, 16 hex bytes and their printable ASCII,
// up to maxBytes.
export function hexDump(b: Uint8Array, maxBytes = 64 * 1024): string {
  const n = Math.min(b.length, maxBytes)
  const lines: string[] = []
  for (let off = 0; off < n; off += 16) {
    const end = Math.min(off + 16, n)
    let hex = ''
    let ascii = ''
    for (let i = off; i < off + 16; i++) {
      if (i < end) {
        hex += b[i].toString(16).padStart(2, '0') + ' '
        ascii += b[i] >= 0x20 && b[i] < 0x7f ? String.fromCharCode(b[i]) : '.'
      } else {
        hex += '   '
      }
      if (i === off + 7) hex += ' '
    }
    lines.push(`${off.toString(16).padStart(8, '0')}  ${hex} |${ascii}|`)
  }
  if (b.length > n) lines.push(`… ${(b.length - n).toLocaleString('en-US')} more bytes`)
  return lines.join('\n')
}
