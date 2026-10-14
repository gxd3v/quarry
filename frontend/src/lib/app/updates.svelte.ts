// Updates: the backend is asked every 30 s for a newer release (GitHub
// answers unchanged ones with 304, outside its rate limit). A newer one is
// announced once with a small toast and stays in the status bar; installing
// it saves the session, swaps the app and restarts it, and the new copy
// reconnects what was open (the backend keeps that list).
import { checkForUpdate, installUpdate, onEvent, takeReconnect, type UpdateInfo } from '../api/server'
import { errorMessage } from '../api/errors'
import { connectAndExpand } from './actions'
import { connections } from './connections.svelte'
import { notify } from './notify.svelte'
import { workspace } from './workspace.svelte'

export const CHECK_EVERY = 30_000
const NOTIFIED_KEY = 'quarry.updateNotified'

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    // storage may be unavailable
  }
}

class Updates {
  info = $state.raw<UpdateInfo | null>(null)
  // The release dialog is open.
  open = $state(false)
  installing = $state(false)
  progress = $state<{ done: number; total: number } | null>(null)
  error = $state('')
  #timer = 0

  get available(): boolean {
    return !!this.info?.newer
  }

  start() {
    if (this.#timer) return
    void this.check(false)
    this.#timer = window.setInterval(() => void this.check(false), CHECK_EVERY)
    onEvent('quarry:update-progress', (done, total) => (this.progress = { done: Number(done), total: Number(total) }))
  }

  async check(manual: boolean) {
    try {
      const info = await checkForUpdate()
      if (info) this.info = info
      if (info?.newer && read(NOTIFIED_KEY) !== info.latest) {
        write(NOTIFIED_KEY, info.latest)
        notify.toast('info', `Quarry ${info.latest} is available`, 8000, [{ label: 'Details', run: () => (this.open = true) }])
      }
      if (!manual) return
      if (info?.newer) this.open = true
      else notify.flash(!info || info.current === 'dev' ? 'Updates are off in development builds' : `Quarry ${info.current} is up to date`)
    } catch (err) {
      if (manual) notify.error(err, 'Check for updates')
    }
  }

  // install saves the session and the open connections, then installs; on
  // success the app quits and the new copy starts.
  async install() {
    const info = this.info
    if (!info?.newer || this.installing) return
    this.installing = true
    this.error = ''
    this.progress = null
    try {
      await workspace.flush()
      await installUpdate(info.latest)
    } catch (err) {
      this.error = errorMessage(err)
      this.installing = false
    }
  }

  // reconnect reopens the connections that were open before an update
  // restart; ones that ask for a password stay closed.
  async reconnect() {
    for (const id of await takeReconnect().catch(() => [])) {
      const cfg = connections.config(id)
      if (cfg && !cfg.askPassword) void connectAndExpand(id).catch(() => {})
    }
  }
}

export const updates = new Updates()
