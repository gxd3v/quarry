import { describe, expect, it } from 'vitest'
import { scopeOfPath, schemaOfObject } from '../sql'
import { flavorOf, redisKey, tableNode, viewsFor } from './table.svelte'

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
    expect(scopeOfPath(['app', 'orders'], 'mongodb')).toEqual({ database: 'app', schema: '' })
    expect(scopeOfPath(['app', 'public', 'tables', 't'])).toEqual({ database: 'app', schema: 'public' })
    expect(scopeOfPath(['HR', 'tables', 'EMP'], 'oracle')).toEqual({ database: '', schema: 'HR' })
    expect(schemaOfObject(['HR', 'tables', 'EMP'], 'oracle')).toBe('HR')
    expect(schemaOfObject(['app', 'public', 'tables', 't'], 'postgres')).toBe('public')
    expect(schemaOfObject(['app', 'orders'], 'mongodb')).toBe('')
    expect(tableNode('oracle', '', 'HR', 'EMP', 'table').path).toEqual(['HR', 'tables', 'EMP'])
    expect(tableNode('postgres', 'app', 'public', 'v', 'view', 'materialized view').path).toEqual(['app', 'public', 'matviews', 'v'])
  })
})
