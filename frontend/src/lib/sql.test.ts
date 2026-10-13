import { describe, expect, it } from 'vitest'
import { quoteIdent, scopeLabel, scopeOfPath, selectAll } from './sql'

describe('sql helpers', () => {
  it('quotes identifiers only when needed', () => {
    expect(quoteIdent('orders')).toBe('orders')
    expect(quoteIdent('order_2')).toBe('order_2')
    expect(quoteIdent('Orders')).toBe('"Orders"')
    expect(quoteIdent('user')).toBe('"user"')
    expect(quoteIdent('2x')).toBe('"2x"')
    expect(quoteIdent('a"b')).toBe('"a""b"')
    expect(quoteIdent('')).toBe('""')
  })

  it('builds select statements', () => {
    expect(selectAll('public', 'orders')).toBe('SELECT * FROM public.orders')
    expect(selectAll('App', 'order')).toBe('SELECT * FROM "App"."order"')
    expect(selectAll('', 't')).toBe('SELECT * FROM t')
  })

  it('reads scopes from node paths', () => {
    expect(scopeOfPath(['app', 'public', 'tables', 'orders'])).toEqual({ database: 'app', schema: 'public' })
    expect(scopeOfPath(['app'])).toEqual({ database: 'app', schema: '' })
    expect(scopeLabel({ database: '', schema: 'public' }, 'postgres')).toBe('postgres.public')
    expect(scopeLabel({ database: 'app', schema: '' })).toBe('app')
  })
})
