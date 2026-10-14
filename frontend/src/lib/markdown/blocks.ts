// A small Markdown subset for release notes, parsed into plain data so it
// renders without innerHTML: headings, bullet lists, paragraphs and inline
// bold, code and links.

export type Inline = { kind: 'text' | 'bold' | 'code'; text: string } | { kind: 'link'; text: string; url: string }

export type Block = { kind: 'heading'; level: number; inline: Inline[] } | { kind: 'list'; items: Inline[][] } | { kind: 'para'; inline: Inline[] }

const INLINE = /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)|(https?:\/\/[^\s)]+)/g

export function inline(s: string): Inline[] {
  const out: Inline[] = []
  let last = 0
  for (const m of s.matchAll(INLINE)) {
    if (m.index > last) out.push({ kind: 'text', text: s.slice(last, m.index) })
    if (m[1] !== undefined) out.push({ kind: 'bold', text: m[1] })
    else if (m[2] !== undefined) out.push({ kind: 'code', text: m[2] })
    else if (m[3] !== undefined) out.push({ kind: 'link', text: m[3], url: m[4] })
    else out.push({ kind: 'link', text: m[5], url: m[5] })
    last = m.index + m[0].length
  }
  if (last < s.length) out.push({ kind: 'text', text: s.slice(last) })
  return out
}

export function blocks(md: string): Block[] {
  const out: Block[] = []
  let para: string[] = []
  const flush = () => {
    if (para.length) out.push({ kind: 'para', inline: inline(para.join(' ')) })
    para = []
  }
  for (const raw of md.replace(/\r\n?/g, '\n').split('\n')) {
    const line = raw.trim()
    const h = /^(#{1,6})\s+(.*)$/.exec(line)
    const li = /^[-*+]\s+(.*)$/.exec(line)
    if (!line) {
      flush()
    } else if (h) {
      flush()
      out.push({ kind: 'heading', level: h[1].length, inline: inline(h[2]) })
    } else if (li) {
      flush()
      const prev = out[out.length - 1]
      if (prev?.kind === 'list') prev.items.push(inline(li[1]))
      else out.push({ kind: 'list', items: [inline(li[1])] })
    } else {
      para.push(line)
    }
  }
  flush()
  return out
}
