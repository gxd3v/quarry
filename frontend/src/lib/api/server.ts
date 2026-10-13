export interface ServerInfo {
  url: string
  token: string
  bench?: boolean
}

declare global {
  interface Window {
    // Minimal shape of the Wails v2 binding; wailsjs/ is deliberately not used.
    go?: { main?: { App?: { ServerInfo(): Promise<ServerInfo> } } }
  }
}

let cached: Promise<ServerInfo> | undefined

// serverInfo resolves the backend address once; a failed attempt is not cached.
export function serverInfo(): Promise<ServerInfo> {
  cached ??= resolve().catch((err: unknown) => {
    cached = undefined
    throw err
  })
  return cached
}

async function resolve(): Promise<ServerInfo> {
  const app = typeof window === 'undefined' ? undefined : window.go?.main?.App
  if (app) return app.ServerInfo()
  const url = import.meta.env.VITE_QUARRY_URL
  if (!url) throw new Error('backend unavailable: no Wails binding and VITE_QUARRY_URL is not set')
  return { url: url.replace(/\/+$/, ''), token: import.meta.env.VITE_QUARRY_TOKEN ?? '' }
}
