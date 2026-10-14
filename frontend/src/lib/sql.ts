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

// Explorer paths by backend: PostgreSQL [database, schema, folder, name],
// Oracle [schema, folder, name], MongoDB [database, collection] and Redis
// [db, folders..., key].

// scopeOfPath reads the session scope of an explorer node.
export function scopeOfPath(path: readonly string[], dbKind = 'postgres'): ScopeLike {
  switch (dbKind) {
    case 'oracle':
      return { database: '', schema: path[0] ?? '' }
    case 'mongodb':
    case 'redis':
      return { database: path[0] ?? '', schema: '' }
  }
  return { database: path[0] ?? '', schema: path[1] ?? '' }
}

// schemaOfObject is the schema that qualifies an object node's name, or ''.
export function schemaOfObject(path: readonly string[], dbKind = 'postgres'): string {
  if (dbKind === 'oracle') return path.length >= 3 ? path[0] : ''
  if (dbKind === 'postgres') return path.length >= 4 ? path[1] : ''
  return ''
}

// scopeLabel renders a scope as "database.schema", falling back to the
// connection's database.
export function scopeLabel(scope: ScopeLike, fallbackDb = ''): string {
  const db = scope.database || fallbackDb
  return [db, scope.schema].filter(Boolean).join('.')
}
