import { describe, expect, it } from 'vitest'
import { formatSql } from './format'

describe('formatSql', () => {
  it('formats PostgreSQL and keeps parameters', async () => {
    const out = await formatSql('select a, b from t where id = :id and x = $1 order by a', 'postgres')
    expect(out).toBe('SELECT\n  a,\n  b\nFROM\n  t\nWHERE\n  id = :id\n  AND x = $1\nORDER BY\n  a')
  })

  it('formats several statements and keeps the trailing newline', async () => {
    const out = await formatSql('select 1; select 2;\n', 'postgres')
    expect(out).toBe('SELECT\n  1;\n\nSELECT\n  2;\n')
  })

  it('uses PL/SQL for Oracle', async () => {
    const out = await formatSql("select * from dual where x = q'[it's]'", 'oracle', { keywordCase: 'lower' })
    expect(out).toContain("q'[it's]'")
    expect(out.startsWith('select')).toBe(true)
  })
})
