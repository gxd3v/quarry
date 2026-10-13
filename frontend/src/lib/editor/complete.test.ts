import { CompletionContext } from '@codemirror/autocomplete'
import { EditorState } from '@codemirror/state'
import { describe, expect, it } from 'vitest'
import { completionSource, matchRange, shouldActivate, toResult, type CompleteReply } from './complete'

describe('completion mapping', () => {
  const doc = 'select o.na from orders o'
  const state = EditorState.create({ doc })
  const reply: CompleteReply = {
    start: 9,
    end: 11,
    items: [
      { label: 'name', insert: 'name', detail: 'text', kind: 'column' },
      { label: 'order_name', insert: '"order_name"', detail: '', kind: 'column' },
      { label: 'nap', insert: '', detail: '', kind: 'weird' },
    ],
  }

  it('maps the range, insert text, kinds and order', () => {
    const r = toResult(reply, state, 11)!
    expect(r.from).toBe(9)
    expect(r.to).toBe(11)
    expect(r.filter).toBe(false)
    expect(r.options.map((o) => [o.label, o.apply, o.detail, o.type, o.boost])).toEqual([
      ['name', 'name', 'text', 'column', -0],
      ['order_name', '"order_name"', undefined, 'column', -1],
      ['nap', 'nap', undefined, 'text', -2],
    ])
    expect(r.getMatch!(r.options[0])).toEqual([0, 2])
    expect(r.getMatch!(r.options[1])).toEqual([6, 8])
  })

  it('clamps ranges and drops empty replies', () => {
    expect(toResult({ ...reply, items: [] }, state, 11)).toBeNull()
    const r = toResult({ ...reply, start: 50, end: 99 }, state, 11)!
    expect([r.from, r.to]).toEqual([11, doc.length])
  })

  it('activates after identifiers and dots only', () => {
    expect(shouldActivate('select o.')).toBe(true)
    expect(shouldActivate('select or')).toBe(true)
    expect(shouldActivate('select ')).toBe(false)
    expect(shouldActivate('select 12')).toBe(false)
    expect(shouldActivate('select 1.')).toBe(false)
    expect(shouldActivate('')).toBe(false)
  })

  it('highlights prefixes, quoted labels and word starts', () => {
    expect(matchRange('Orders', 'or')).toEqual([0, 2])
    expect(matchRange('"Orders"', '"Or')).toEqual([1, 3])
    expect(matchRange('order_items', 'it')).toEqual([6, 8])
    expect(matchRange('orders', '')).toEqual([])
    expect(matchRange('orders', 'xy')).toEqual([])
  })
})

describe('completionSource', () => {
  const ctx = (doc: string, explicit = false) => new CompletionContext(EditorState.create({ doc }), doc.length, explicit)

  it('calls the fetcher only when it should', async () => {
    const calls: [string, number][] = []
    const source = completionSource(() => async (script, cursor) => {
      calls.push([script, cursor])
      return { start: 7, end: 9, items: [{ label: 'orders', insert: 'orders', detail: '', kind: 'table' }] }
    })
    expect(await source(ctx('select '))).toBeNull()
    expect(calls).toEqual([])
    const r = await source(ctx('select or'))
    expect(calls).toEqual([['select or', 9]])
    expect(r?.options[0].label).toBe('orders')
    await source(ctx('select ', true))
    expect(calls).toHaveLength(2)
  })

  it('returns null when the fetch fails or there is no fetcher', async () => {
    expect(await completionSource(() => undefined)(ctx('x', true))).toBeNull()
    expect(
      await completionSource(() => async () => {
        throw new Error('boom')
      })(ctx('x', true)),
    ).toBeNull()
  })
})
