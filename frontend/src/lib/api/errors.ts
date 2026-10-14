import { Code, ConnectError } from '@connectrpc/connect'

// errorMessage is the user-facing text of an RPC or any other failure.
export function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || Code[err.code]
  if (err instanceof Error) return err.message
  return String(err)
}

export function errorCode(err: unknown): Code | undefined {
  return err instanceof ConnectError ? err.code : undefined
}

// isAbort reports a request that this side gave up on.
export function isAbort(err: unknown): boolean {
  if (err instanceof DOMException && err.name === 'AbortError') return true
  return err instanceof ConnectError && err.code === Code.Canceled
}

// needsPassword reports a connect that failed for want of a (right) password.
// PostgreSQL rejects a bad password as Unavailable with SQLSTATE 28P01.
export function needsPassword(err: unknown): boolean {
  if (!(err instanceof ConnectError)) return false
  if (err.code === Code.Unauthenticated) return true
  return err.code === Code.Unavailable && /password authentication failed|no password supplied|28P01/i.test(err.rawMessage)
}

export function notConnected(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition && /connection is not open/.test(err.rawMessage)
}

export function unknownTab(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.NotFound && /unknown tab|tab is closed/.test(err.rawMessage)
}

// needsConfirmation reports a Run the server refused until the user confirms
// its risks (writes on prod, unbounded or destructive statements).
export function needsConfirmation(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition && err.rawMessage.startsWith('confirmation required:')
}

export function readOnly(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition && /read-only/.test(err.rawMessage)
}

export function tabBusy(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition && /tab is busy/.test(err.rawMessage)
}

// Cancelled is thrown when the user dismisses a prompt that an action needed.
export class Cancelled extends Error {
  constructor() {
    super('cancelled')
    this.name = 'Cancelled'
  }
}
