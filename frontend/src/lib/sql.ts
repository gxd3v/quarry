// PostgreSQL reserved key words (those that cannot be bare column or table names).
const RESERVED = new Set(
  (
    'all analyse analyze and any array as asc asymmetric authorization binary both case cast check collate ' +
    'collation column concurrently constraint create cross current_catalog current_date current_role ' +
    'current_schema current_time current_timestamp current_user default deferrable desc distinct do else end ' +
    'except false fetch for foreign freeze from full grant group having ilike in initially inner intersect into ' +
    'is isnull join lateral leading left like limit localtime localtimestamp natural not notnull null offset on ' +
    'only or order outer overlaps placing primary references returning right select session_user similar some ' +
    'symmetric system_user table tablesample then to trailing true union unique user using variadic verbose when ' +
    'where window with'
  ).split(' '),
)

// quoteIdent quotes a PostgreSQL identifier only when it needs it.
export function quoteIdent(name: string): string {
  if (/^[a-z_][a-z0-9_$]*$/.test(name) && !RESERVED.has(name)) return name
  return `"${name.replaceAll('"', '""')}"`
}

export function selectAll(schema: string, table: string): string {
  const t = quoteIdent(table)
  return `SELECT * FROM ${schema ? `${quoteIdent(schema)}.${t}` : t}`
}

export interface ScopeLike {
  database: string
  schema: string
}

// scopeOfPath reads the scope of a PostgreSQL explorer node: path[0] is the
// database and path[1] the schema.
export function scopeOfPath(path: readonly string[]): ScopeLike {
  return { database: path[0] ?? '', schema: path[1] ?? '' }
}

// scopeLabel renders a scope as "database.schema", falling back to the
// connection's database.
export function scopeLabel(scope: ScopeLike, fallbackDb = ''): string {
  const db = scope.database || fallbackDb
  return [db, scope.schema].filter(Boolean).join('.')
}
