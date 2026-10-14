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
  SaveExportDialog(defaultName: string, format: string): Promise<string>
  OpenFileDialog(title: string, kind: string, pattern: string): Promise<string>
  // Shows a file in the system file manager.
  RevealPath(path: string): Promise<void>
  // Lets the window close after the frontend saved its state.
  ConfirmClose(): Promise<void>
}

// Minimal shape of the Wails v2 runtime events API.
interface Runtime {
  EventsOn(name: string, cb: (...args: unknown[]) => void): () => void
}

declare global {
  interface Window {
    go?: { main?: { App?: Partial<AppBinding> } }
    runtime?: Runtime
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

// saveExportDialog asks where to write an export; '' means cancelled.
export async function saveExportDialog(defaultName: string, format: string): Promise<string> {
  const a = app()
  if (!a?.SaveExportDialog) throw new Error('file dialogs need the desktop app')
  return a.SaveExportDialog(defaultName, format)
}

// openFileDialog asks for a file to read; '' means cancelled.
export async function openFileDialog(title: string, kind = '', pattern = ''): Promise<string> {
  const a = app()
  if (!a?.OpenFileDialog) throw new Error('file dialogs need the desktop app')
  return a.OpenFileDialog(title, kind, pattern)
}

// canReveal reports whether revealPath works here (desktop app only).
export function canReveal(): boolean {
  return !!app()?.RevealPath
}

export async function revealPath(path: string): Promise<void> {
  await app()?.RevealPath?.(path)
}

// onBeforeClose runs fn when the user closes the window, then lets it close.
// The backend closes the window anyway if fn takes more than a few seconds.
export function onBeforeClose(fn: () => Promise<unknown>): () => void {
  const rt = typeof window === 'undefined' ? undefined : window.runtime
  if (!rt) return () => {}
  return rt.EventsOn('quarry:before-close', () => {
    void fn()
      .catch(() => {})
      .finally(() => app()?.ConfirmClose?.())
  })
}
