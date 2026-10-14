import { SvelteMap } from 'svelte/reactivity'
import { connectionClient } from '../api/clients'
import { Cancelled, errorMessage, isAbort, needsPassword, notConnected } from '../api/errors'
import {
  ConnectionState,
  type Connection,
  type ConnectionConfig,
  type ConnectionStatus,
  type DriverInfo,
} from '../gen/quarry/v1/connection_pb'
import { notify } from './notify.svelte'
import { prompts } from './prompts.svelte'

// SaveSecrets says what SaveConnection does with the stored secrets.
export interface SaveSecrets {
  password?: string
  forgetPassword: boolean
  sshPassword?: string
  sshPassphrase?: string
}

export { ConnectionState }

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

export function stateLabel(s: ConnectionState | undefined): string {
  switch (s) {
    case ConnectionState.CONNECTED:
      return 'connected'
    case ConnectionState.CONNECTING:
      return 'connecting'
    case ConnectionState.LOST:
      return 'lost'
    case ConnectionState.FAILED:
      return 'failed'
    default:
      return 'disconnected'
  }
}

class Connections {
  list = $state.raw<Connection[]>([])
  drivers = $state.raw<DriverInfo[]>([])
  loaded = $state(false)
  loadError = $state('')
  readonly statuses = new SvelteMap<string, ConnectionStatus>()
  readonly #byId = $derived(new Map(this.list.map((c) => [c.config!.id, c])))
  readonly #pending = new Map<string, Promise<void>>()
  #watching = false

  get(id: string): Connection | undefined {
    return this.#byId.get(id)
  }

  config(id: string): ConnectionConfig | undefined {
    return this.#byId.get(id)?.config
  }

  name(id: string): string {
    return this.config(id)?.name ?? 'unknown connection'
  }

  env(id: string): string {
    return this.config(id)?.env || 'local'
  }

  status(id: string): ConnectionStatus | undefined {
    return this.statuses.get(id)
  }

  state(id: string): ConnectionState {
    return this.statuses.get(id)?.state ?? ConnectionState.DISCONNECTED
  }

  connected(id: string): boolean {
    return this.state(id) === ConnectionState.CONNECTED
  }

  async load() {
    try {
      const [conns, drivers] = await Promise.all([
        connectionClient().listConnections({}),
        this.drivers.length ? null : connectionClient().listDrivers({}),
      ])
      this.list = conns.connections.filter((c) => c.config)
      if (drivers) this.drivers = drivers.drivers
      for (const c of this.list) if (c.status && !this.statuses.has(c.config!.id)) this.statuses.set(c.config!.id, c.status)
      this.loadError = ''
    } catch (err) {
      this.loadError = errorMessage(err)
      throw err
    } finally {
      this.loaded = true
    }
  }

  async save(config: ConnectionConfig, secrets: SaveSecrets): Promise<Connection> {
    const res = await connectionClient().saveConnection({ config, ...secrets })
    const saved = res.connection!
    const id = saved.config!.id
    const i = this.list.findIndex((c) => c.config!.id === id)
    this.list = i < 0 ? [...this.list, saved] : this.list.map((c, j) => (j === i ? saved : c))
    if (saved.status) this.statuses.set(id, saved.status)
    return saved
  }

  async remove(id: string) {
    await connectionClient().deleteConnection({ id })
    this.list = this.list.filter((c) => c.config!.id !== id)
    this.statuses.delete(id)
  }

  // connect opens a connection, asking for a password when the server needs one.
  async connect(id: string) {
    let password: string | undefined
    for (let attempt = 0; ; attempt++) {
      try {
        const res = await connectionClient().connect({ id, password })
        if (res.status) this.statuses.set(id, res.status)
        return
      } catch (err) {
        if (!needsPassword(err) || attempt >= 5) throw err
        const pw = await prompts.askPassword(`Password for ${this.name(id)}`, password === undefined ? '' : errorMessage(err))
        if (pw === null) throw new Cancelled()
        password = pw
      }
    }
  }

  async disconnect(id: string) {
    await connectionClient().disconnect({ id })
  }

  // ensure connects id unless it is open; concurrent callers share one attempt.
  ensure(id: string): Promise<void> {
    if (this.connected(id)) return Promise.resolve()
    let p = this.#pending.get(id)
    if (!p) {
      p = this.connect(id).finally(() => this.#pending.delete(id))
      this.#pending.set(id, p)
    }
    return p
  }

  // withConnection runs f, connecting first and retrying once when the
  // server says the connection is not open.
  async withConnection<T>(id: string, f: () => Promise<T>): Promise<T> {
    if (!this.connected(id)) await this.ensure(id)
    try {
      return await f()
    } catch (err) {
      if (!notConnected(err)) throw err
      this.statuses.delete(id)
      await this.ensure(id)
      return f()
    }
  }

  // watch keeps the app's single WatchStatus stream open, reconnecting with
  // backoff when it drops.
  async watch(signal?: AbortSignal) {
    if (this.#watching) return
    this.#watching = true
    let delay = 500
    while (!signal?.aborted) {
      try {
        let first = true
        for await (const msg of connectionClient().watchStatus({}, { signal })) {
          delay = 500
          if (first) {
            first = false
            this.statuses.clear()
          }
          this.#apply(msg.statuses, msg.deleted)
        }
      } catch (err) {
        if (signal?.aborted || isAbort(err)) break
      }
      await sleep(delay)
      delay = Math.min(delay * 2, 10_000)
    }
    this.#watching = false
  }

  #apply(statuses: ConnectionStatus[], deleted: string[]) {
    let unknown = false
    for (const s of statuses) {
      const prev = this.statuses.get(s.id)?.state
      this.statuses.set(s.id, s)
      if (!this.#byId.has(s.id)) unknown = true
      const name = this.name(s.id)
      if (s.state === ConnectionState.LOST && prev !== ConnectionState.LOST) {
        notify.toast('error', `Connection ${name} lost${s.error ? `: ${s.error}` : ''}`)
      } else if (s.state === ConnectionState.CONNECTED && prev === ConnectionState.LOST) {
        notify.toast('success', `Connection ${name} is back`)
      }
    }
    for (const id of deleted) this.statuses.delete(id)
    if (deleted.length) this.list = this.list.filter((c) => !deleted.includes(c.config!.id))
    if (unknown && this.loaded) this.load().catch(() => {})
  }
}

export const connections = new Connections()
