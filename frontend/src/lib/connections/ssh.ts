// SSH tunnel settings of a connection. They live in ConnectionConfig.options
// (ssh_* keys, read by the backend's tunnel.FromOptions); the password and
// key passphrase go to the keyring through SaveConnection.

export const SSH_KEYS = ['ssh_host', 'ssh_port', 'ssh_user', 'ssh_key', 'ssh_jump', 'ssh_known_hosts', 'ssh_agent', 'ssh_accept_new'] as const
export type SshKey = (typeof SSH_KEYS)[number]

// Options stored as "true" or left out.
export type SshFlag = 'ssh_agent' | 'ssh_accept_new'

type Options = Readonly<Record<string, string>>

const set = (v: string | undefined) => !!v && v.trim() !== ''

// sshEnabled reports whether the backend opens a tunnel: it needs ssh_host.
export function sshEnabled(o: Options): boolean {
  return set(o.ssh_host)
}

// sshConfigured reports whether any SSH option is set, e.g. to open the
// dialog's SSH section.
export function sshConfigured(o: Options): boolean {
  return SSH_KEYS.some((k) => set(o[k]))
}

export function sshFlag(o: Options, key: SshFlag): boolean {
  return /^(true|1|yes|on)$/i.test((o[key] ?? '').trim())
}

// withSshFlag returns options with the flag stored as "true", or removed.
export function withSshFlag(o: Options, key: SshFlag, on: boolean): Record<string, string> {
  const next = { ...o }
  if (on) next[key] = 'true'
  else delete next[key]
  return next
}

// withoutSsh drops every SSH option.
export function withoutSsh(o: Options): Record<string, string> {
  const next = { ...o }
  for (const k of SSH_KEYS) delete next[k]
  return next
}

// A jump host: [user@]host[:port], host possibly a bracketed IPv6 address.
const JUMP = /^(?:[^@\s,]+@)?(?:\[[0-9a-fA-F:.]+\]|[^@\s:,[\]]+)(?::\d{1,5})?$/

export function jumpHosts(s: string): string[] {
  return s
    .split(',')
    .map((j) => j.trim())
    .filter(Boolean)
}

// validateSsh returns the first problem with the SSH options, or ''.
export function validateSsh(o: Options): string {
  if (!sshConfigured(o)) return ''
  if (!sshEnabled(o)) return 'SSH host is required to use a tunnel'
  const port = (o.ssh_port ?? '').trim()
  if (port) {
    const n = /^\d+$/.test(port) ? Number(port) : NaN
    if (!(n >= 1 && n <= 65535)) return 'SSH port must be between 1 and 65535'
  }
  for (const j of jumpHosts(o.ssh_jump ?? '')) {
    if (!JUMP.test(j)) return `Jump host "${j}" is not user@host:port`
  }
  return ''
}

export interface SshSecrets {
  sshPassword: string
  sshPasswordTouched: boolean
  sshPassphrase: string
  sshPassphraseTouched: boolean
}

// sshSecretsForSave says what SaveConnection does with the SSH secrets:
// unset keeps the stored one, '' removes it, anything else replaces it.
export function sshSecretsForSave(f: SshSecrets): { sshPassword?: string; sshPassphrase?: string } {
  const out: { sshPassword?: string; sshPassphrase?: string } = {}
  if (f.sshPasswordTouched) out.sshPassword = f.sshPassword
  if (f.sshPassphraseTouched) out.sshPassphrase = f.sshPassphrase
  return out
}
