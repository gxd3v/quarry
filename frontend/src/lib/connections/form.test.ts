import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ConnectionConfigSchema, ParseURIResponseSchema } from '../gen/quarry/v1/connection_pb'
import {
  applyParsed,
  configFromForm,
  emptyForm,
  formFromConfig,
  parsePort,
  passwordForSave,
  passwordForTest,
  validate,
} from './form'

describe('connection form', () => {
  it('round-trips a config', () => {
    const cfg = create(ConnectionConfigSchema, {
      id: 'x',
      name: 'Local',
      kind: 'postgres',
      env: 'prod',
      host: 'db',
      port: 5433,
      user: 'u',
      database: 'app',
      readOnly: true,
      group: 'g',
      favorite: true,
      askPassword: true,
      options: { sslmode: 'require' },
    })
    const f = formFromConfig(cfg, false)
    expect(f.port).toBe('5433')
    expect(f.passwordMode).toBe('ask')
    expect(configFromForm(f)).toEqual(cfg)
  })

  it('drops empty options and trims fields', () => {
    const f = { ...emptyForm(), name: ' A ', host: ' h ', options: { sslmode: '' } }
    const c = configFromForm(f)
    expect(c.name).toBe('A')
    expect(c.host).toBe('h')
    expect(c.port).toBe(0)
    expect(c.options).toEqual({})
  })

  it('validates', () => {
    expect(validate(emptyForm())).toBe('Name is required')
    expect(validate({ ...emptyForm(), name: 'a', port: '70000' })).toMatch(/Port/)
    expect(validate({ ...emptyForm(), name: 'a', port: '5432' })).toBe('')
    expect(parsePort('')).toBe(0)
    expect(parsePort('5x')).toBeNaN()
  })

  it('applies a parsed URI', () => {
    const p = create(ParseURIResponseSchema, {
      config: { kind: 'postgres', host: 'db', port: 6543, user: 'bob', database: 'shop', options: { sslmode: 'disable' } },
      password: 'pw',
    })
    const f = applyParsed({ ...emptyForm(), env: 'dev' }, p)
    expect(f).toMatchObject({
      name: 'bob@db/shop',
      env: 'dev',
      host: 'db',
      port: '6543',
      password: 'pw',
      passwordTouched: true,
      options: { sslmode: 'disable' },
    })
    expect(applyParsed({ ...emptyForm(), name: 'Keep' }, p).name).toBe('Keep')
  })

  it('decides what happens to the password', () => {
    const stored = { ...emptyForm(), id: 'x', hasPassword: true }
    expect(passwordForSave(stored)).toEqual({ forgetPassword: false })
    expect(passwordForSave({ ...stored, passwordTouched: true, password: 'n' })).toEqual({ password: 'n', forgetPassword: false })
    expect(passwordForSave({ ...stored, passwordTouched: true })).toEqual({ forgetPassword: true })
    expect(passwordForSave({ ...stored, passwordMode: 'ask', passwordTouched: true, password: 'n' })).toEqual({
      forgetPassword: false,
    })
    expect(passwordForTest(stored)).toBeUndefined()
    expect(passwordForTest({ ...emptyForm(), password: 'p' })).toBe('p')
  })
})
