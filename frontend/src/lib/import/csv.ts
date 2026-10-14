// CSV import form logic (ImportService): formats, column mapping and checks.

export const DELIMITERS = [
  { value: ',', label: 'Comma (,)' },
  { value: ';', label: 'Semicolon (;)' },
  { value: '\t', label: 'Tab' },
  { value: '|', label: 'Pipe (|)' },
] as const

export const ENCODINGS = ['UTF-8', 'UTF-8 BOM', 'UTF-16LE', 'UTF-16BE', 'Windows-1252'] as const

// keepMapping keeps the user's mapping when a new preview has the same file
// columns, and takes the server's suggestion otherwise.
export function keepMapping(prevHeader: readonly string[], prev: readonly string[], header: readonly string[], suggested: readonly string[]): string[] {
  const same = prevHeader.length === header.length && prevHeader.every((h, i) => h === header[i])
  return same && prev.length === header.length ? [...prev] : header.map((_, i) => suggested[i] ?? '')
}

// mappingProblem returns why a mapping cannot be imported, or ''.
export function mappingProblem(mapping: readonly string[]): string {
  const used = mapping.filter(Boolean)
  if (!used.length) return 'Map at least one file column to a table column'
  const seen = new Set<string>()
  for (const c of used) {
    if (seen.has(c)) return `Column ${c} is mapped from more than one file column`
    seen.add(c)
  }
  return ''
}

// nullText is the request's null_text: undefined means empty cells are NULL.
export function nullText(mode: 'empty' | 'text' | 'none', text: string): string | undefined {
  if (mode === 'empty') return undefined
  // A marker no cell can hold keeps every cell, empty ones included, as text.
  if (mode === 'none') return '\u0000'
  return text
}
