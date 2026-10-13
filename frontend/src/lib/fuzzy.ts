// A small fuzzy matcher for the command palette and "go to object": the
// query must appear in order (case-insensitively) in the text; matches at
// word starts and in runs score higher, and shorter texts win ties.

export interface FuzzyMatch {
  score: number
  // Indices of the matched characters in the text.
  positions: number[]
}

const START = 10
const BOUNDARY = 8
const CAMEL = 6
const RUN = 6
// Penalty per skipped character between two matches, capped.
const GAP = 1
const MAX_GAP = 6
// Penalty per character before the first match, capped.
const LEAD = 0.5
const MAX_LEAD = 6

function isSep(c: string): boolean {
  return ' _.-/\\:("'.includes(c)
}

function bonus(text: string, i: number): number {
  if (i === 0) return START
  const prev = text[i - 1]
  if (isSep(prev)) return BOUNDARY
  const c = text[i]
  if (prev >= 'a' && prev <= 'z' && c >= 'A' && c <= 'Z') return CAMEL
  return 0
}

// fuzzy matches query against text, or returns null when it does not match.
// Whitespace in the query is ignored; an empty query matches with score 0.
export function fuzzy(query: string, text: string): FuzzyMatch | null {
  const q = query.replace(/\s+/g, '').toLowerCase()
  if (!q) return { score: 0, positions: [] }
  const t = text.toLowerCase()
  const n = t.length
  const m = q.length
  if (m > n) return null
  for (let i = 0, j = 0; j < m; i++) {
    if (i === n) return null
    if (t[i] === q[j]) j++
  }

  // best[j][i]: best score with q[j] matched at t[i]; back[j][i]: where q[j-1] matched.
  const NONE = -Infinity
  const best: Float64Array[] = []
  const back: Int32Array[] = []
  for (let j = 0; j < m; j++) {
    const row = new Float64Array(n).fill(NONE)
    const from = new Int32Array(n).fill(-1)
    const prev = j > 0 ? best[j - 1] : null
    // Over predecessors k < i - 1: max of prev[k] + k*GAP (linear penalty)
    // and max of prev[k] (capped penalty); together they give the exact
    // max of prev[k] - min((i-1-k)*GAP, MAX_GAP).
    let lin = NONE
    let linAt = -1
    let cap = NONE
    let capAt = -1
    for (let i = j; i < n; i++) {
      if (prev && i >= 2 && prev[i - 2] !== NONE) {
        const k = i - 2
        if (prev[k] + k * GAP > lin) {
          lin = prev[k] + k * GAP
          linAt = k
        }
        if (prev[k] > cap) {
          cap = prev[k]
          capAt = k
        }
      }
      if (t[i] !== q[j]) continue
      const gain = 1 + bonus(text, i)
      if (!prev) {
        row[i] = gain - Math.min(i * LEAD, MAX_LEAD)
        continue
      }
      let score = NONE
      let at = -1
      if (prev[i - 1] !== NONE) {
        score = prev[i - 1] + gain + RUN
        at = i - 1
      }
      if (linAt >= 0 && lin - (i - 1) * GAP + gain > score) {
        score = lin - (i - 1) * GAP + gain
        at = linAt
      }
      if (capAt >= 0 && cap - MAX_GAP + gain > score) {
        score = cap - MAX_GAP + gain
        at = capAt
      }
      row[i] = score
      from[i] = at
    }
    best.push(row)
    back.push(from)
  }

  const last = best[m - 1]
  let end = -1
  for (let i = 0; i < n; i++) if (last[i] !== NONE && (end < 0 || last[i] > last[end])) end = i
  if (end < 0) return null
  const positions = new Array<number>(m)
  for (let j = m - 1, i = end; j >= 0; j--) {
    positions[j] = i
    i = back[j][i]
  }
  return { score: last[end] - n * 0.01, positions }
}

export interface Ranked<T> {
  item: T
  score: number
  positions: number[]
}

// rank filters items by query and sorts them best first, keeping the input
// order for ties and for an empty query; limit caps the result.
export function rank<T>(items: readonly T[], query: string, key: (item: T) => string, limit = Infinity): Ranked<T>[] {
  const out: (Ranked<T> & { i: number })[] = []
  for (let i = 0; i < items.length; i++) {
    const m = fuzzy(query, key(items[i]))
    if (m) out.push({ item: items[i], score: m.score, positions: m.positions, i })
  }
  if (query.trim()) out.sort((a, b) => b.score - a.score || a.i - b.i)
  return out.slice(0, limit).map(({ item, score, positions }) => ({ item, score, positions }))
}

// segments splits text into runs of matched and unmatched characters, for
// highlighting.
export function segments(text: string, positions: readonly number[]): { text: string; hit: boolean }[] {
  const hits = new Set(positions)
  const out: { text: string; hit: boolean }[] = []
  for (let i = 0; i < text.length; i++) {
    const hit = hits.has(i)
    const last = out.at(-1)
    if (last && last.hit === hit) last.text += text[i]
    else out.push({ text: text[i], hit })
  }
  return out
}
