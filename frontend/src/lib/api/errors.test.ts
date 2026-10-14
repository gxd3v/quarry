import { Code, ConnectError } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { errorMessage, isAbort, needsConfirmation, needsPassword, notConnected, sshRefused, tabBusy, unimplemented, unknownTab } from './errors'

describe('error classification', () => {
  it('uses the raw message', () => {
    expect(errorMessage(new ConnectError('Name is required', Code.InvalidArgument))).toBe('Name is required')
    expect(errorMessage(new Error('x'))).toBe('x')
  })

  it('detects password prompts', () => {
    expect(needsPassword(new ConnectError('password required', Code.Unauthenticated))).toBe(true)
    expect(
      needsPassword(new ConnectError('FATAL: password authentication failed for user "x" (SQLSTATE 28P01)', Code.Unavailable)),
    ).toBe(true)
    expect(needsPassword(new ConnectError('dial tcp: refused', Code.Unavailable))).toBe(false)
  })

  it('tells SSH login failures from database password prompts', () => {
    const ssh = new ConnectError('ssh: authentication failed for me@bastion (tried: password)', Code.PermissionDenied)
    expect(sshRefused(ssh)).toBe(true)
    expect(needsPassword(ssh)).toBe(false)
    expect(sshRefused(new ConnectError('password required', Code.Unauthenticated))).toBe(false)
    expect(unimplemented(new ConnectError('redis sessions cannot explain statements', Code.Unimplemented))).toBe(true)
    expect(unimplemented(new Error('x'))).toBe(false)
  })

  it('detects tab and connection states', () => {
    expect(notConnected(new ConnectError('pg: connection is not open', Code.FailedPrecondition))).toBe(true)
    expect(tabBusy(new ConnectError('tab is busy', Code.FailedPrecondition))).toBe(true)
    expect(unknownTab(new ConnectError('unknown tab "a"', Code.NotFound))).toBe(true)
    expect(unknownTab(new ConnectError('unknown connection "a"', Code.NotFound))).toBe(false)
    expect(needsConfirmation(new ConnectError('confirmation required: DELETE without WHERE', Code.FailedPrecondition))).toBe(true)
    expect(needsConfirmation(new ConnectError('tab is busy', Code.FailedPrecondition))).toBe(false)
  })

  it('detects aborts', () => {
    expect(isAbort(new DOMException('x', 'AbortError'))).toBe(true)
    expect(isAbort(new ConnectError('x', Code.Canceled))).toBe(true)
    expect(isAbort(new Error('x'))).toBe(false)
  })
})
