import { describe, expect, it } from 'vitest'
import { emptyForm, validate } from './form'
import { sshConfigured, sshEnabled, sshFlag, sshSecretsForSave, validateSsh, withSshFlag, withoutSsh } from './ssh'

describe('ssh options', () => {
  it('validates host, port and jump hosts', () => {
    expect(validateSsh({})).toBe('')
    expect(validateSsh({ ssh_user: 'me' })).toMatch(/host is required/)
    expect(validateSsh({ ssh_host: 'b', ssh_port: '70000' })).toMatch(/port/)
    expect(validateSsh({ ssh_host: 'b', ssh_jump: 'me@j1:22, [::1]:2222, j3' })).toBe('')
    expect(validateSsh({ ssh_host: 'b', ssh_jump: 'me@@j1' })).toMatch(/Jump host "me@@j1"/)
    expect(validate({ ...emptyForm(), name: 'x', options: { ssh_port: '22' } })).toMatch(/SSH host/)
  })

  it('keeps flags as "true" or nothing', () => {
    const on = withSshFlag({ ssh_host: 'b' }, 'ssh_agent', true)
    expect(on).toEqual({ ssh_host: 'b', ssh_agent: 'true' })
    expect(sshFlag(on, 'ssh_agent')).toBe(true)
    expect(withSshFlag(on, 'ssh_agent', false)).toEqual({ ssh_host: 'b' })
    expect(sshEnabled(on) && sshConfigured(on)).toBe(true)
    expect(withoutSsh({ ...on, sslmode: 'require' })).toEqual({ sslmode: 'require' })
  })

  it('sends only edited secrets', () => {
    const f = { sshPassword: '', sshPasswordTouched: false, sshPassphrase: 'p', sshPassphraseTouched: true }
    expect(sshSecretsForSave(f)).toEqual({ sshPassphrase: 'p' })
    expect(sshSecretsForSave({ ...f, sshPasswordTouched: true })).toEqual({ sshPassword: '', sshPassphrase: 'p' })
  })
})
