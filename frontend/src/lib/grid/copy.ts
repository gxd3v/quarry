// Clipboard formats for grid selections. They follow internal/export: NULL is
// empty in CSV/TSV (and an empty string is quoted to stay distinct), JSON
// keeps numbers, booleans and JSON documents typed, Markdown escapes pipes
// and line breaks, and SQL literals quote everything that is not a number.
import { quoteIdent } from '../sql'
import { compactJson } from '../values'
import type { Cell, ValueKind } from './buffer'

export type CopyFormat = 'tsv' | 'csv' | 'json' | 'markdown' | 'sql'

export const COPY_FORMATS: { format: CopyFormat; label: string }[] = [
  { format: 'tsv', label: 'TSV' },
  { format: 'csv', label: 'CSV' },
  { format: 'json', label: 'JSON' },
  { format: 'markdown', label: 'Markdown' },
  { format: 'sql', label: 'SQL INSERT' },
]

export interface CopyColumn {
  name: string
  type: string
  numeric: boolean
  kind?: ValueKind
}

export interface CopyOptions {
  // CSV, TSV and Markdown header row; default true.
  header?: boolean
  // SQL INSERT target, verbatim; default "t".
  table?: string
  // "postgres", "oracle" or "" (generic).
  dialect?: string
}

// copyPlain is what Ctrl+C copies: a single cell's text as is, or TSV rows
// without a header or a trailing line break.
export function copyPlain(rows: readonly (readonly Cell[])[]): string {
  if (rows.length === 1 && rows[0].length === 1) return rows[0][0] ?? ''
  return rows.map((r) => r.map((v) => delimited(v, '\t')).join('\t')).join('\n')
}

export function formatCopy(format: CopyFormat, cols: readonly CopyColumn[], rows: readonly (readonly Cell[])[], opts: CopyOptions = {}): string {
  const header = opts.header ?? true
  switch (format) {
    case 'tsv':
      return delimitedRows(cols, rows, '\t', '\n', header)
    case 'csv':
      return delimitedRows(cols, rows, ',', '\r\n', header)
    case 'json':
      return jsonRows(cols, rows)
    case 'markdown':
      return markdownRows(cols, rows, header)
    case 'sql':
      return sqlRows(cols, rows, opts.table || 't', opts.dialect ?? 'postgres')
  }
}

function delimited(v: Cell, delim: string): string {
  if (v === null) return ''
  if (v === '' || v.includes('"') || v.includes('\n') || v.includes('\r') || v.includes(delim)) return `"${v.replaceAll('"', '""')}"`
  return v
}

function delimitedRows(cols: readonly CopyColumn[], rows: readonly (readonly Cell[])[], delim: string, eol: string, header: boolean): string {
  const out: string[] = []
  // Header names are quoted only when special, like export.
  if (header) out.push(cols.map((c) => (c.name === '' ? '' : delimited(c.name, delim))).join(delim) + eol)
  for (const r of rows) out.push(cols.map((_, i) => delimited(r[i] ?? null, delim)).join(delim) + eol)
  return out.join('')
}

const NUMBER = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/

export function isNumberLiteral(s: string): boolean {
  return NUMBER.test(s)
}

function isNumericKind(c: CopyColumn): boolean {
  return c.kind === 'int' || c.kind === 'float' || c.kind === 'number' || (!c.kind && c.numeric)
}

// uniqueNames suffixes duplicate column names (id, id_2) for JSON keys.
export function uniqueNames(cols: readonly CopyColumn[]): string[] {
  const seen = new Set<string>()
  return cols.map((c) => {
    let name = c.name
    for (let n = 2; seen.has(name); n++) name = `${c.name}_${n}`
    seen.add(name)
    return name
  })
}

// isoTime turns a grid timestamp ("2026-10-13 14:05:00.25", local time) into
// RFC 3339 with the local offset, or returns it unchanged.
export function isoTime(s: string): string {
  const o = localOffset(s)
  return o === null ? s : s.replace(' ', 'T') + o
}

function localOffset(s: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})(\.\d+)?$/.exec(s)
  if (!m) return null
  const d = new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6])
  if (Number.isNaN(d.getTime())) return null
  const off = -d.getTimezoneOffset()
  if (off === 0) return 'Z'
  const a = Math.abs(off)
  return `${off < 0 ? '-' : '+'}${String(Math.floor(a / 60)).padStart(2, '0')}:${String(a % 60).padStart(2, '0')}`
}

function jsonValue(c: CopyColumn, v: Cell): string {
  if (v === null) return 'null'
  if (isNumericKind(c)) {
    if (isNumberLiteral(v)) return v
    if (c.kind === 'float') return 'null'
    return JSON.stringify(v)
  }
  switch (c.kind) {
    case 'bool':
      return v === 'true' ? 'true' : 'false'
    case 'json':
      try {
        JSON.parse(v)
        return compactJson(v)
      } catch {
        return JSON.stringify(v)
      }
    case 'time':
      return JSON.stringify(isoTime(v))
  }
  return JSON.stringify(v)
}

function jsonRows(cols: readonly CopyColumn[], rows: readonly (readonly Cell[])[]): string {
  if (!rows.length) return '[]\n'
  const keys = uniqueNames(cols).map((k) => JSON.stringify(k))
  const lines = rows.map((r) => '{' + cols.map((c, i) => `${keys[i]}:${jsonValue(c, r[i] ?? null)}`).join(',') + '}')
  return '[\n' + lines.join(',\n') + '\n]\n'
}

function mdEscape(s: string): string {
  return s.replace(/[\\|]/g, '\\$&').replace(/\r\n|\n|\r/g, '<br>').replaceAll('\t', ' ')
}

function width(s: string): number {
  let n = 0
  for (const _ of s) n++
  return n
}

function markdownRows(cols: readonly CopyColumn[], rows: readonly (readonly Cell[])[], header: boolean): string {
  const head = cols.map((c) => (header ? mdEscape(c.name) : ''))
  const body = rows.map((r) => cols.map((_, i) => (r[i] == null ? 'NULL' : mdEscape(r[i]!))))
  const right = cols.map((c, i) => isNumericKind(c) && rows.some((r) => r[i] != null))
  const widths = cols.map((_, i) => Math.max(3, width(head[i]), ...body.map((r) => width(r[i]))))
  const line = (cells: string[]) =>
    '|' +
    cells
      .map((v, i) => {
        const pad = ' '.repeat(Math.max(0, widths[i] - width(v)))
        return ' ' + (right[i] ? pad + v : v + pad) + ' |'
      })
      .join('') +
    '\n'
  const sep = '|' + widths.map((w, i) => ' ' + (right[i] ? '-'.repeat(w - 1) + ':' : '-'.repeat(w)) + ' |').join('') + '\n'
  return line(head) + sep + body.map(line).join('')
}

// sqlIdent quotes a column name for the dialect: PostgreSQL (and generic SQL)
// keeps lower-case non-reserved names bare, Oracle upper-case ones.
export function sqlIdent(name: string, dialect: string): string {
  if (dialect === 'oracle') return /^[A-Z_][A-Z0-9_$#]*$/.test(name) ? name : `"${name.replaceAll('"', '""')}"`
  return quoteIdent(name)
}

function sqlString(v: string): string {
  return `'${v.replaceAll("'", "''")}'`
}

// sqlLiteral renders a cell as a SQL literal for the dialect.
export function sqlLiteral(c: CopyColumn, v: Cell, dialect: string): string {
  if (v === null) return 'NULL'
  const oracle = dialect === 'oracle'
  if (isNumericKind(c)) {
    if (isNumberLiteral(v)) return v
    if (oracle && c.kind === 'float') {
      if (v === 'NaN') return 'BINARY_DOUBLE_NAN'
      return v.startsWith('-') ? '-BINARY_DOUBLE_INFINITY' : 'BINARY_DOUBLE_INFINITY'
    }
    return sqlString(v)
  }
  switch (c.kind) {
    case 'bool':
      if (oracle) return v === 'true' ? '1' : '0'
      return v === 'true' ? 'TRUE' : 'FALSE'
    case 'bytes':
      if (oracle && v.startsWith('\\x')) return `HEXTORAW('${v.slice(2)}')`
      break
    case 'time': {
      const off = localOffset(v)
      if (off === null) break
      if (oracle) return `TIMESTAMP '${v}${off === 'Z' ? '' : ' ' + off}'`
      if (dialect === 'postgres') return sqlString(v + (off === 'Z' ? '+00:00' : off))
    }
  }
  return sqlString(v)
}

function sqlRows(cols: readonly CopyColumn[], rows: readonly (readonly Cell[])[], table: string, dialect: string): string {
  const into = `INSERT INTO ${table} (${cols.map((c) => sqlIdent(c.name, dialect)).join(', ')}) VALUES (`
  return rows.map((r) => into + cols.map((c, i) => sqlLiteral(c, r[i] ?? null, dialect)).join(', ') + ');\n').join('')
}

// guessTable returns the table a simple "SELECT ... FROM t" reads, for
// INSERT copies, or '' when the statement is not that simple.
export function guessTable(sql: string): string {
  const ident = String.raw`(?:"(?:[^"]|"")+"|[A-Za-z_][\w$]*)`
  const re = new RegExp(String.raw`^\s*select\b[\s\S]*?\bfrom\s+(${ident})(?:\s*\.\s*(${ident}))?\s*(?:$|;|\bwhere\b|\border\b|\blimit\b|\boffset\b|\bfetch\b|\bfor\b)`, 'i')
  const m = re.exec(sql)
  if (!m) return ''
  return m[2] ? `${m[1]}.${m[2]}` : m[1]
}
