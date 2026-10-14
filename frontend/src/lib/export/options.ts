// The export dialog's form and its mapping to ExportService options.
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ExportOptionsSchema } from '../gen/quarry/v1/export_pb'

export type ExportFormat = 'csv' | 'tsv' | 'json' | 'ndjson' | 'sql' | 'markdown'

export const EXPORT_FORMATS: { format: ExportFormat; label: string; ext: string }[] = [
  { format: 'csv', label: 'CSV', ext: '.csv' },
  { format: 'tsv', label: 'TSV', ext: '.tsv' },
  { format: 'json', label: 'JSON', ext: '.json' },
  { format: 'ndjson', label: 'NDJSON', ext: '.ndjson' },
  { format: 'sql', label: 'SQL INSERT', ext: '.sql' },
  { format: 'markdown', label: 'Markdown', ext: '.md' },
]

export interface ExportForm {
  format: ExportFormat
  // CSV only; '' means ",".
  delimiter: string
  header: boolean
  nullText: string
  quoteAll: boolean
  crlf: boolean
  bom: boolean
  // SQL only; '' means the source table.
  table: string
  // SQL only: rows per INSERT; 0 means one INSERT per row.
  batchRows: number
  path: string
}

export const DEFAULT_FORM: ExportForm = {
  format: 'csv',
  delimiter: ',',
  header: true,
  nullText: '',
  quoteAll: false,
  // RFC 4180 line endings, the server's default for CSV.
  crlf: true,
  bom: false,
  table: '',
  batchRows: 100,
  path: '',
}

// Which options apply to a format.
export function applies(format: ExportFormat) {
  return {
    delimiter: format === 'csv',
    header: format === 'csv' || format === 'tsv' || format === 'markdown',
    nullText: format === 'csv' || format === 'tsv' || format === 'markdown',
    quoteAll: format === 'csv',
    table: format === 'sql',
  }
}

// exportOptions maps the form to the request, sending only what applies.
// Booleans are always explicit (proto3 has no presence for them).
export function exportOptions(f: ExportForm): MessageInitShape<typeof ExportOptionsSchema> {
  const a = applies(f.format)
  const delimiter = f.delimiter === '\\t' ? '\t' : f.delimiter
  return {
    format: f.format,
    delimiter: a.delimiter && delimiter !== ',' ? delimiter : '',
    header: a.header ? f.header : false,
    nullText: a.nullText ? f.nullText : '',
    quoteAll: a.quoteAll ? f.quoteAll : false,
    crlf: f.crlf,
    bom: f.bom,
    table: a.table ? f.table.trim() : '',
    batchRows: a.table ? Math.max(0, Math.floor(f.batchRows) || 0) : 0,
  }
}

export function extensionOf(format: ExportFormat): string {
  return EXPORT_FORMATS.find((x) => x.format === format)?.ext ?? '.txt'
}

// withExtension swaps a path's export extension for the format's, keeping
// other extensions (e.g. "data.2026" becomes "data.2026.csv").
export function withExtension(path: string, format: ExportFormat): string {
  if (!path) return path
  const known = /\.(csv|tsv|json|ndjson|sql|md|markdown|txt)$/i
  return path.replace(known, '') + extensionOf(format)
}

// fileName suggests a file name for an export of name.
export function fileName(name: string, format: ExportFormat): string {
  const base = name.replace(/[\\/:*?"<>|]+/g, '_').trim() || 'export'
  return base + extensionOf(format)
}

// validate returns what is wrong with the form, or ''.
export function validate(f: ExportForm): string {
  if (!f.path.trim()) return 'Choose where to save the file'
  if (!/^([a-zA-Z]:[\\/]|\/|\\\\)/.test(f.path.trim())) return 'The path must be absolute'
  const d = f.delimiter === '\\t' ? '\t' : f.delimiter
  if (f.format === 'csv' && ([...d].length > 1 || /["\r\n]/.test(d))) return 'The delimiter must be one character other than a quote or line break'
  return ''
}
