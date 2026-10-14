// LISTEN/NOTIFY: a script tab can listen to channels on its connection; the
// notifications show in its results panel. Each listener holds a stream.
import { notifyClient } from '../api/clients'
import { errorMessage, isAbort, unimplemented } from '../api/errors'
import type { ScopeLike } from '../sql'
import { connections } from './connections.svelte'

// Notifications kept per listener; older ones are dropped.
export const MAX_NOTIFICATIONS = 1000

export interface Received {
  at: Date
  channel: string
  payload: string
  pid: number
}

// parseChannels splits "a, b" into channel names, without repeats; names
// may contain spaces, as quoted PostgreSQL identifiers can.
export function parseChannels(s: string): string[] {
  return [...new Set(s.split(',').map((c) => c.trim()).filter(Boolean))]
}

export class Listener {
  readonly connId: string
  readonly scope: ScopeLike
  readonly channels: string[]
  state = $state<'starting' | 'listening' | 'stopped' | 'failed'>('starting')
  error = $state('')
  items = $state.raw<Received[]>([])
  // Notifications dropped to keep MAX_NOTIFICATIONS.
  dropped = $state(0)
  #ctrl = new AbortController()

  constructor(connId: string, scope: ScopeLike, channels: string[]) {
    this.connId = connId
    this.scope = scope
    this.channels = channels
  }

  async start() {
    try {
      await connections.withConnection(this.connId, async () => {
        const req = { connId: this.connId, scope: { database: this.scope.database, schema: this.scope.schema }, channels: this.channels }
        for await (const res of notifyClient().listen(req, { signal: this.#ctrl.signal })) {
          if (res.event.case === 'listening') this.state = 'listening'
          else if (res.event.case === 'notification') this.#add(res.event.value)
        }
      })
      this.state = 'stopped'
    } catch (err) {
      if (this.#ctrl.signal.aborted || isAbort(err)) {
        this.state = 'stopped'
        return
      }
      this.state = 'failed'
      this.error = unimplemented(err) ? `${connections.name(this.connId)} has no notifications (only PostgreSQL does)` : errorMessage(err)
    }
  }

  #add(n: { at?: { seconds: bigint; nanos: number }; channel: string; payload: string; pid: number }) {
    const at = n.at ? new Date(Number(n.at.seconds) * 1000 + Math.floor(n.at.nanos / 1e6)) : new Date()
    const next = [...this.items, { at, channel: n.channel, payload: n.payload, pid: n.pid }]
    if (next.length > MAX_NOTIFICATIONS) {
      this.dropped += next.length - MAX_NOTIFICATIONS
      next.splice(0, next.length - MAX_NOTIFICATIONS)
    }
    this.items = next
  }

  clear() {
    this.items = []
    this.dropped = 0
  }

  stop() {
    this.#ctrl.abort()
  }
}
