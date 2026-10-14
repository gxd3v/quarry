import { create, type MessageInitShape } from '@bufbuild/protobuf'
import {
  ConnectionConfigSchema,
  type ConnectionConfig,
  type ParseURIResponse,
} from '../gen/quarry/v1/connection_pb'
import { validateSsh } from './ssh'

export const ENVS = ['local', 'dev', 'qa', 'prod'] as const
export type Env = (typeof ENVS)[number]

export const SSL_MODES = ['', 'disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full'] as const

export type PasswordMode = 'keyring' | 'ask'

// ConnForm is the editable state of the connection dialog. Numbers are kept
// as strings while typing.
export interface ConnForm {
  id: string
  name: string
  kind: string
  env: Env
  host: string
  port: string
  user: string
  password: string
  // The password field was edited (an untouched field keeps the stored one).
  passwordTouched: boolean
  passwordMode: PasswordMode
  hasPassword: boolean
  database: string
  readOnly: boolean
  group: string
  favorite: boolean
  options: Record<string, string>
  // SSH tunnel secrets; untouched fields keep the stored ones.
  sshPassword: string
  sshPasswordTouched: boolean
  sshPassphrase: string
  sshPassphraseTouched: boolean
}

const noSshSecrets = { sshPassword: '', sshPasswordTouched: false, sshPassphrase: '', sshPassphraseTouched: false }

export function emptyForm(kind = 'postgres'): ConnForm {
  return {
    id: '',
    name: '',
    kind,
    env: 'local',
    host: 'localhost',
    port: '',
    user: '',
    password: '',
    passwordTouched: false,
    passwordMode: 'keyring',
    hasPassword: false,
    database: '',
    readOnly: false,
    group: '',
    favorite: false,
    options: {},
    ...noSshSecrets,
  }
}

export function formFromConfig(c: ConnectionConfig, hasPassword = false): ConnForm {
  return {
    id: c.id,
    name: c.name,
    kind: c.kind,
    env: (ENVS as readonly string[]).includes(c.env) ? (c.env as Env) : 'local',
    host: c.host,
    port: c.port ? String(c.port) : '',
    user: c.user,
    password: '',
    passwordTouched: false,
    passwordMode: c.askPassword ? 'ask' : 'keyring',
    hasPassword,
    database: c.database,
    readOnly: c.readOnly,
    group: c.group,
    favorite: c.favorite,
    options: { ...c.options },
    ...noSshSecrets,
  }
}

// applyParsed fills the form from a parsed URI, keeping what the URI does not
// say (name, env, group...).
export function applyParsed(f: ConnForm, p: ParseURIResponse): ConnForm {
  const c = p.config
  if (!c) return f
  const next: ConnForm = {
    ...f,
    kind: c.kind || f.kind,
    host: c.host,
    port: c.port ? String(c.port) : '',
    user: c.user,
    database: c.database,
    options: { ...f.options, ...c.options },
  }
  if (p.password !== undefined) {
    next.password = p.password
    next.passwordTouched = true
    next.passwordMode = 'keyring'
  }
  if (!next.name.trim()) next.name = defaultName(next)
  return next
}

export function defaultName(f: Pick<ConnForm, 'user' | 'host' | 'database'>): string {
  const target = f.database ? `${f.host}/${f.database}` : f.host
  return f.user ? `${f.user}@${target}` : target
}

// parsePort returns the port number, 0 for the default, or NaN when invalid.
export function parsePort(s: string): number {
  const t = s.trim()
  if (t === '') return 0
  if (!/^\d+$/.test(t)) return NaN
  const n = Number(t)
  return n >= 1 && n <= 65535 ? n : NaN
}

// validate returns the first problem the server would also report, or ''.
export function validate(f: ConnForm): string {
  if (!f.name.trim()) return 'Name is required'
  if (!f.kind) return 'Database type is required'
  if (Number.isNaN(parsePort(f.port))) return 'Port must be between 1 and 65535'
  return validateSsh(f.options)
}

export function configFromForm(f: ConnForm): ConnectionConfig {
  const options: Record<string, string> = {}
  for (const [k, v] of Object.entries(f.options)) if (v.trim()) options[k] = v.trim()
  const init: MessageInitShape<typeof ConnectionConfigSchema> = {
    id: f.id,
    name: f.name.trim(),
    kind: f.kind,
    env: f.env,
    host: f.host.trim(),
    port: parsePort(f.port) || 0,
    user: f.user.trim(),
    database: f.database.trim(),
    readOnly: f.readOnly,
    group: f.group.trim(),
    favorite: f.favorite,
    askPassword: f.passwordMode === 'ask',
    options,
  }
  return create(ConnectionConfigSchema, init)
}

// passwordForSave says what SaveConnection should do with the password:
// keep the stored one, store a new one, or forget it.
export function passwordForSave(f: ConnForm): { password?: string; forgetPassword: boolean } {
  if (f.passwordMode === 'ask' || !f.passwordTouched) return { forgetPassword: false }
  if (f.password === '' && f.hasPassword) return { forgetPassword: true }
  return { password: f.password, forgetPassword: false }
}

// passwordForTest is the password a test should use; undefined uses the stored one.
export function passwordForTest(f: ConnForm): string | undefined {
  if (f.passwordTouched || f.passwordMode === 'ask' || !f.id) return f.password
  return undefined
}
