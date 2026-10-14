import { describe, expect, it } from 'vitest'
import { decide, paramLabel, riskLabel, type InspectLike } from './guard'

const script = 'select 1;\nupdate t set a = 1 where id = 2;\ndelete from t;'
const at = (s: string) => ({ start: script.indexOf(s), end: script.indexOf(s) + s.length })

const base: InspectLike = {
  statements: [
    { range: at('select 1'), risk: 'none', verb: 'SELECT', reason: '' },
    { range: at('update t set a = 1 where id = 2'), risk: 'write', verb: 'UPDATE', reason: '' },
    { range: at('delete from t'), risk: 'unbounded_write', verb: 'DELETE', reason: 'DELETE without WHERE' },
  ],
  params: [],
  needsConfirmation: false,
  blocked: false,
  env: 'dev',
}

describe('decide', () => {
  it('runs plain scripts without asking', () => {
    expect(decide({ ...base, statements: base.statements.slice(0, 1) }, script, 'c')).toEqual({ kind: 'run', params: [], confirm: null, prod: false })
  })

  it('lists the risky statements when confirmation is needed', () => {
    const d = decide({ ...base, needsConfirmation: true, env: 'PROD' }, script, 'c')
    expect(d.kind).toBe('run')
    if (d.kind !== 'run') return
    expect(d.prod).toBe(true)
    expect(d.confirm).toEqual([
      { verb: 'UPDATE', reason: '', risk: 'write', text: 'update t set a = 1 where id = 2' },
      { verb: 'DELETE', reason: 'DELETE without WHERE', risk: 'unbounded_write', text: 'delete from t' },
    ])
  })

  it('blocks writes on read-only connections', () => {
    const d = decide({ ...base, blocked: true, needsConfirmation: true }, script, 'reporting')
    expect(d).toEqual({ kind: 'blocked', message: 'reporting is read-only: this run would modify data (UPDATE, DELETE). Nothing was executed.' })
  })

  it('asks for parameters in order of use', () => {
    const d = decide({ ...base, statements: [], params: ['a', '1'] }, 'select :a::int + $1::int', 'c')
    expect(d.kind === 'run' && d.params).toEqual(['a', '1'])
  })

  it('labels parameters and risks', () => {
    expect(paramLabel('1', 'postgres')).toBe('$1')
    expect(paramLabel('1', 'oracle')).toBe(':1')
    expect(paramLabel('name', 'postgres')).toBe(':name')
    expect(riskLabel('destructive')).toBe('destructive')
    expect(riskLabel('none')).toBe('')
  })
})
