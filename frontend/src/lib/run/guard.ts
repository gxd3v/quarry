// What to do before a run, from QueryService.Inspect: refuse it on a
// read-only connection, ask for bind parameters, and ask for confirmation
// of risky statements (writes on prod, unbounded writes, destructive DDL).

export interface StatementCheckLike {
  range?: { start: number; end: number }
  risk: string
  verb: string
  reason: string
}

export interface InspectLike {
  statements: readonly StatementCheckLike[]
  params: readonly string[]
  needsConfirmation: boolean
  blocked: boolean
  env: string
}

export interface RiskItem {
  verb: string
  reason: string
  risk: string
  // The statement's text, shortened.
  text: string
}

export type RunDecision =
  | { kind: 'blocked'; message: string }
  | { kind: 'run'; params: string[]; confirm: RiskItem[] | null; prod: boolean }

const MAX_TEXT = 160

// decide reads an Inspect response for a script; connName names the
// connection in messages.
export function decide(res: InspectLike, script: string, connName: string): RunDecision {
  const risky = res.statements.filter((s) => s.risk && s.risk !== 'none')
  if (res.blocked) {
    const verbs = [...new Set(risky.map((s) => s.verb).filter(Boolean))]
    return {
      kind: 'blocked',
      message: `${connName} is read-only: this run would modify data${verbs.length ? ` (${verbs.join(', ')})` : ''}. Nothing was executed.`,
    }
  }
  const items = risky.map((s) => ({ verb: s.verb || s.risk.toUpperCase(), reason: s.reason, risk: s.risk, text: snippet(script, s.range) }))
  return {
    kind: 'run',
    params: [...res.params],
    confirm: res.needsConfirmation ? items : null,
    prod: res.env.toLowerCase() === 'prod',
  }
}

function snippet(script: string, r?: { start: number; end: number }): string {
  if (!r) return ''
  const s = script.slice(r.start, r.end).replace(/\s+/g, ' ').trim()
  return s.length > MAX_TEXT ? s.slice(0, MAX_TEXT - 1) + '…' : s
}

// paramLabel shows a bare parameter name as written in the script: $1 on
// PostgreSQL, :name (and Oracle's :1) otherwise.
export function paramLabel(name: string, dialect: string): string {
  return /^\d+$/.test(name) && dialect === 'postgres' ? `$${name}` : `:${name}`
}

// riskLabel is a short, human name for a risk level.
export function riskLabel(risk: string): string {
  switch (risk) {
    case 'write':
      return 'writes'
    case 'ddl':
      return 'changes schema'
    case 'unbounded_write':
      return 'affects every row'
    case 'destructive':
      return 'destructive'
  }
  return ''
}
