import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Value } from './gen/quarry/v1/common_pb'

// Display text longer than this is clipped; the grid never shows more anyway.
export const MAX_CELL_CHARS = 500
export const MAX_BYTES_SHOWN = 32

const HEX = Array.from({ length: 256 }, (_, i) => i.toString(16).padStart(2, '0'))
const CONTROL = /[\u0000-\u001f\u007f]/
const CONTROL_ALL = /\r\n|[\u0000-\u001f\u007f]/g
const NUMERIC_TYPE =
  /^(_?int[248]?|integer|bigint|smallint|tinyint|mediumint|numeric|decimal|number|real|double|float[48]?|binary_(float|double)|money|oid|(small|big)?serial[248]?)\b/i

// formatValue renders a cell as single-line display text, or null for SQL NULL.
// The result never contains C0 control characters, which ResultBuffer relies on.
// When the display text loses information (clipped, control characters
// replaced, long bytes), onLossy receives the full value.
export function formatValue(v: Value | undefined, onLossy?: (full: string | Uint8Array) => void): string | null {
  const s = rawText(v, MAX_CELL_CHARS)
  if (s === null) return null
  const d = displayText(s)
  if (onLossy) {
    const k = v!.kind
    if (k.case === 'bytes') {
      if (k.value.length > MAX_BYTES_SHOWN) onLossy(k.value.slice())
    } else if (k.case === 'json') {
      if (d !== s || s.length > MAX_CELL_CHARS) onLossy(k.value)
    } else if (d !== s) {
      onLossy(s)
    }
  }
  return d
}

// bytesHex renders bytes in full in PostgreSQL's hex format.
export function bytesHex(b: Uint8Array): string {
  const parts = new Array<string>(b.length + 1)
  parts[0] = '\\x'
  for (let i = 0; i < b.length; i++) parts[i + 1] = HEX[b[i]]
  return parts.join('')
}

export function isNumericType(type: string): boolean {
  return NUMERIC_TYPE.test(type.trim())
}

function rawText(v: Value | undefined, limit: number): string | null {
  const k = v?.kind
  switch (k?.case) {
    case undefined:
    case 'null':
      return null
    case 'bool':
      return k.value ? 'true' : 'false'
    case 'int':
      return k.value.toString()
    case 'float':
      return String(k.value)
    case 'number':
    case 'text':
      return k.value
    case 'bytes':
      return formatBytes(k.value)
    case 'time':
      return formatTime(k.value)
    case 'json':
      return compactJson(k.value, limit)
    case 'array':
      return formatArray(k.value.values)
  }
}

function displayText(s: string): string {
  if (s.length > MAX_CELL_CHARS) s = s.slice(0, MAX_CELL_CHARS - 1) + '…'
  if (!CONTROL.test(s)) return s
  return s.replace(CONTROL_ALL, (c) => (c === '\t' ? ' ' : c === '\n' || c === '\r' || c === '\r\n' ? '↵' : '\ufffd'))
}

export function formatBytes(b: Uint8Array): string {
  const n = Math.min(b.length, MAX_BYTES_SHOWN)
  let s = '\\x'
  for (let i = 0; i < n; i++) s += HEX[b[i]]
  return b.length > n ? s + '…' : s
}

// formatTime renders a timestamp in local time as `YYYY-MM-DD HH:MM:SS[.fraction]`.
export function formatTime(ts: Timestamp): string {
  const d = new Date(Number(ts.seconds) * 1000)
  if (Number.isNaN(d.getTime())) return `@${ts.seconds}`
  const p = (n: number, w = 2) => String(n).padStart(w, '0')
  let s =
    `${p(d.getFullYear(), 4)}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  if (ts.nanos > 0) s += '.' + p(ts.nanos, 9).replace(/0+$/, '')
  return s
}

// compactJson strips insignificant whitespace without parsing, so large
// integers keep their exact digits. It stops early once `limit` is exceeded.
export function compactJson(s: string, limit = Infinity): string {
  const parts: string[] = []
  let len = 0
  let start = 0
  let inString = false
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (inString) {
      if (c === 92) i++
      else if (c === 34) inString = false
    } else if (c === 34) {
      inString = true
    } else if (c === 32 || c === 10 || c === 13 || c === 9) {
      if (i > start) {
        parts.push(s.slice(start, i))
        len += i - start
        if (len > limit) return parts.join('')
      }
      start = i + 1
    }
  }
  if (start < s.length) parts.push(s.slice(start))
  return parts.join('')
}

// formatArray renders a PostgreSQL-style array literal: {1,NULL,"a b"}.
export function formatArray(values: Value[]): string {
  return `{${values.map(arrayElement).join(',')}}`
}

function arrayElement(v: Value): string {
  if (v.kind.case === 'array') return formatArray(v.kind.value.values)
  const s = rawText(v, Infinity)
  if (s === null) return 'NULL'
  if (s === '' || /[{},"\\\s]/.test(s) || /^null$/i.test(s)) return `"${s.replace(/["\\]/g, '\\$&')}"`
  return s
}
