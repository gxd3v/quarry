export interface ServerInfo {
  url: string
  token: string
  bench?: boolean
}

// Minimal shape of the Wails v2 binding; wailsjs/ is deliberately not used.
interface AppBinding {
  ServerInfo(): Promise<ServerInfo>
  // Native file dialogs; "" means cancelled.
  OpenScriptDialog(): Promise<string>
  SaveScriptDialog(defaultName: string): Promise<string>
}

declare global {
  interface Window {
    go?: { main?: { App?: Partial<AppBinding> } }
  }
}

let cached: Promise<ServerInfo> | undefined

function app(): Partial<AppBinding> | undefined {
  return typeof window === 'undefined' ? undefined : window.go?.main?.App
}

// serverInfo resolves the backend address once; a failed attempt is not cached.
export function serverInfo(): Promise<ServerInfo> {
  cached ??= resolve().catch((err: unknown) => {
    cached = undefined
    throw err
  })
  return cached
}

async function resolve(): Promise<ServerInfo> {
  const a = app()
  if (a?.ServerInfo) return a.ServerInfo()
  const url = import.meta.env.VITE_QUARRY_URL
  if (!url) throw new Error('backend unavailable: no Wails binding and VITE_QUARRY_URL is not set')
  return { url: url.replace(/\/+$/, ''), token: import.meta.env.VITE_QUARRY_TOKEN ?? '' }
}

// openScriptDialog asks for a script to open; '' means cancelled.
export async function openScriptDialog(): Promise<string> {
  const a = app()
  if (!a?.OpenScriptDialog) throw new Error('file dialogs need the desktop app')
  return a.OpenScriptDialog()
}

// saveScriptDialog asks where to save a script; '' means cancelled.
export async function saveScriptDialog(defaultName: string): Promise<string> {
  const a = app()
  if (!a?.SaveScriptDialog) throw new Error('file dialogs need the desktop app')
  return a.SaveScriptDialog(defaultName)
}
