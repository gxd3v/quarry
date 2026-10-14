// SQL formatting (D-14) with sql-formatter, loaded on first use.

export interface FormatOptions {
  keywordCase?: 'upper' | 'lower' | 'preserve'
  tabWidth?: number
}

const DIALECT: Record<string, string> = { postgres: 'postgresql', oracle: 'plsql' }

// formatSql formats one or more statements in the connection's dialect,
// keeping bind parameters (:name, $1, ?) as they are. Text after the last
// statement (a trailing comment or newline) is kept.
export async function formatSql(text: string, dbKind: string, o: FormatOptions = {}): Promise<string> {
  const { format } = await import('sql-formatter')
  const language = (DIALECT[dbKind] ?? 'sql') as 'postgresql' | 'plsql' | 'sql'
  const out = format(text, {
    language,
    keywordCase: o.keywordCase ?? 'upper',
    tabWidth: o.tabWidth ?? 2,
    linesBetweenQueries: 1,
    paramTypes: language === 'postgresql' ? { named: [':'], numbered: ['$'], positional: true } : { named: [':'], positional: true },
  })
  return /\n\s*$/.test(text) ? `${out}\n` : out
}
