import { describe, expect, it } from 'vitest'
import { flavorOf, redisKey, scopeOfNode, viewsFor } from './table.svelte'

describe('data tabs by backend', () => {
  it('decodes marked Redis keys', () => {
    expect(redisKey(['0', 'a', '\u0000k:a:b'])).toBe('a:b')
    expect(redisKey(['0', '\u0000x:c3a9'])).toBe('é')
    expect(redisKey(['0', 'folder'])).toBe('')
  })

  it('picks views and scopes', () => {
    expect(flavorOf('mongodb')).toBe('mongo')
    expect(flavorOf('oracle')).toBe('sql')
    expect(viewsFor('redis').map((v) => v.view)).toEqual(['data', 'stats'])
    expect(viewsFor('sql').length).toBe(7)
    expect(scopeOfNode('mongo', ['app', 'orders'])).toEqual({ database: 'app', schema: '' })
    expect(scopeOfNode('sql', ['app', 'public', 'tables', 't'])).toEqual({ database: 'app', schema: 'public' })
  })
})
