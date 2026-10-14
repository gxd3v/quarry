// Update checks: once a day at start, or on demand. Quarry never replaces
// itself; a newer release is announced with a link to its page.
import { checkForUpdate, openURL } from '../api/server'
import { errorMessage } from '../api/errors'
import { notify } from './notify.svelte'

const LAST_CHECK_KEY = 'quarry.updateCheck'
const DAY = 24 * 60 * 60 * 1000

// due reports whether a daily check should run, given the last one.
export function due(last: string | null, now: number): boolean {
  const t = Number(last)
  return !last || !Number.isFinite(t) || now - t >= DAY || t > now
}

export async function checkUpdates(manual: boolean) {
  if (!manual) {
    try {
      if (!due(localStorage.getItem(LAST_CHECK_KEY), Date.now())) return
      localStorage.setItem(LAST_CHECK_KEY, String(Date.now()))
    } catch {
      return
    }
  }
  try {
    const info = await checkForUpdate()
    if (info?.newer) {
      notify.toast('info', `Quarry ${info.latest} is available (you have ${info.current})`, 20000, [{ label: 'Download', run: () => openURL(info.url) }])
    } else if (manual) {
      notify.flash(!info || info.current === 'dev' ? 'Development builds do not check for updates' : `Quarry ${info.current} is the latest version`)
    }
  } catch (err) {
    if (manual) notify.error(err, 'Check for updates')
    else console.debug('update check:', errorMessage(err))
  }
}
