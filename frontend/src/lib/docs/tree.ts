// Documents as trees: MongoDB results carry each document as relaxed
// Extended JSON, shown here as collapsible mongosh-like trees.

export type DocNode =
  | { kind: 'object'; entries: [string, DocNode][] }
  | { kind: 'array'; items: DocNode[] }
  | { kind: 'scalar'; text: string; type: 'string' | 'number' | 'bool' | 'null' | 'special' }

// Extended JSON wrappers shown as one mongosh value.
const SPECIAL: Record<string, (v: unknown) => string | null> = {
  $oid: (v) => (typeof v === 'string' ? `ObjectId("${v}")` : null),
  $date: (v) => {
    if (typeof v === 'string') return `ISODate("${v}")`
    if (v && typeof v === 'object' && '$numberLong' in v) return `ISODate("${new Date(Number((v as { $numberLong: string }).$numberLong)).toISOString()}")`
    return null
  },
  $numberDecimal: (v) => (typeof v === 'string' ? `NumberDecimal("${v}")` : null),
  $numberLong: (v) => (typeof v === 'string' ? `NumberLong("${v}")` : null),
  $numberInt: (v) => (typeof v === 'string' ? v : null),
  $numberDouble: (v) => (typeof v === 'string' ? v : null),
  $uuid: (v) => (typeof v === 'string' ? `UUID("${v}")` : null),
  $timestamp: (v) => (v && typeof v === 'object' ? `Timestamp(${(v as { t: number }).t}, ${(v as { i: number }).i})` : null),
  $minKey: () => 'MinKey()',
  $maxKey: () => 'MaxKey()',
}

function special(o: Record<string, unknown>): string | null {
  const keys = Object.keys(o)
  if (keys.length === 1 && keys[0] in SPECIAL) return SPECIAL[keys[0]](o[keys[0]])
  if ('$binary' in o && keys.length === 1) {
    const b = o.$binary as { base64?: string; subType?: string }
    return b?.subType === '04' ? `UUID(base64 "${b.base64}")` : `BinData(${parseInt(b?.subType ?? '0', 16)}, "${b?.base64 ?? ''}")`
  }
  if ('$regularExpression' in o && keys.length === 1) {
    const r = o.$regularExpression as { pattern: string; options: string }
    return `/${r.pattern}/${r.options}`
  }
  return null
}

export function toTree(v: unknown): DocNode {
  if (v === null || v === undefined) return { kind: 'scalar', text: 'null', type: 'null' }
  if (Array.isArray(v)) return { kind: 'array', items: v.map(toTree) }
  switch (typeof v) {
    case 'string':
      return { kind: 'scalar', text: JSON.stringify(v), type: 'string' }
    case 'number':
      return { kind: 'scalar', text: String(v), type: 'number' }
    case 'boolean':
      return { kind: 'scalar', text: String(v), type: 'bool' }
    case 'object': {
      const o = v as Record<string, unknown>
      const s = special(o)
      if (s !== null) return { kind: 'scalar', text: s, type: 'special' }
      return { kind: 'object', entries: Object.entries(o).map(([k, x]) => [k, toTree(x)]) }
    }
  }
  return { kind: 'scalar', text: String(v), type: 'special' }
}

// parseDoc parses a document cell; null when it is not JSON.
export function parseDoc(text: string | null | undefined): DocNode | null {
  if (!text) return null
  try {
    return toTree(JSON.parse(text))
  } catch {
    return null
  }
}

// summary renders a collapsed node on one line, cut at max characters.
export function summary(n: DocNode, max = 80): string {
  const out = render(n, max)
  return out.length > max ? `${out.slice(0, max - 1)}…` : out
}

function render(n: DocNode, budget: number): string {
  switch (n.kind) {
    case 'scalar':
      return n.text
    case 'array': {
      let s = '['
      for (const [i, it] of n.items.entries()) {
        if (s.length > budget) return `${s}…]`
        s += (i ? ', ' : '') + render(it, budget - s.length)
      }
      return `${s}]`
    }
    case 'object': {
      let s = '{ '
      for (const [i, [k, v]] of n.entries.entries()) {
        if (s.length > budget) return `${s}… }`
        s += (i ? ', ' : '') + `${key(k)}: ${render(v, budget - s.length)}`
      }
      return n.entries.length ? `${s} }` : '{}'
    }
  }
}

// key quotes field names that are not plain identifiers, as mongosh does.
export function key(k: string): string {
  return /^[A-Za-z_$][\w$]*$/.test(k) ? k : JSON.stringify(k)
}

// idOf is the _id of a document tree, rendered, or ''.
export function idOf(n: DocNode | null): string {
  if (n?.kind !== 'object') return ''
  const id = n.entries.find(([k]) => k === '_id')
  return id ? summary(id[1], 60) : ''
}
