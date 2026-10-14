import { FitAddon } from '@xterm/addon-fit'
import { Terminal, type ITheme } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { shellClient } from '../api/clients'
import { errorMessage, isAbort, needsPassword } from '../api/errors'
import type { ScopeLike } from '../sql'

export type TerminalState = 'starting' | 'running' | 'exited' | 'failed'

const encoder = new TextEncoder()

// themeFromCSS builds the xterm palette from the app's theme variables, so
// terminals follow the current theme.
export function themeFromCSS(get: (name: string) => string): ITheme {
  const v = (n: string) => get(`--${n}`).trim()
  return {
    background: v('crust'),
    foreground: v('text'),
    cursor: v('rosewater'),
    cursorAccent: v('crust'),
    selectionBackground: v('surface2'),
    black: v('surface1'),
    red: v('red'),
    green: v('green'),
    yellow: v('yellow'),
    blue: v('blue'),
    magenta: v('pink'),
    cyan: v('teal'),
    white: v('subtext1'),
    brightBlack: v('surface2'),
    brightRed: v('red'),
    brightGreen: v('green'),
    brightYellow: v('yellow'),
    brightBlue: v('blue'),
    brightMagenta: v('pink'),
    brightCyan: v('teal'),
    brightWhite: v('subtext0'),
  }
}

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name)
}

// TerminalSession is the native CLI of one connection running in a PTY on the
// backend. The xterm instance outlives the component showing it, so switching
// tabs keeps the scrollback; its element is moved into whichever host shows it.
export class TerminalSession {
  readonly connId: string
  readonly scope: ScopeLike
  state = $state<TerminalState>('starting')
  display = $state('')
  // Exit code or start error.
  status = $state('')
  readonly element: HTMLDivElement
  readonly term: Terminal
  readonly #fit = new FitAddon()
  #shellId = ''
  #controller: AbortController | null = null
  #askPassword: () => Promise<string | null>
  #retheme = () => (this.term.options.theme = themeFromCSS(cssVar))

  constructor(connId: string, scope: ScopeLike, askPassword: () => Promise<string | null>) {
    this.connId = connId
    this.scope = scope
    this.#askPassword = askPassword
    this.element = document.createElement('div')
    this.element.className = 'xterm-host'
    this.term = new Terminal({
      fontFamily: cssVar('--font-mono') || 'monospace',
      fontSize: 13,
      cursorBlink: true,
      scrollback: 5000,
      theme: themeFromCSS(cssVar),
    })
    this.term.loadAddon(this.#fit)
    this.term.open(this.element)
    this.term.onData((d) => this.#input(encoder.encode(d)))
    this.term.onBinary((d) => this.#input(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff)))
    window.addEventListener('quarry:theme', this.#retheme)
    this.term.onResize(({ cols, rows }) => {
      if (this.#shellId) shellClient().shellResize({ shellId: this.#shellId, cols, rows }).catch(() => {})
    })
  }

  // fit resizes the terminal to its host; called when the host's size changes.
  fit() {
    if (this.element.isConnected && this.element.clientWidth > 0) this.#fit.fit()
  }

  // start launches the CLI, asking for the password when the connection
  // needs one; it resolves when the shell ends.
  async start(password?: string): Promise<void> {
    this.#controller?.abort()
    const ctrl = new AbortController()
    this.#controller = ctrl
    this.state = 'starting'
    this.status = ''
    this.#shellId = ''
    this.fit()
    try {
      const stream = shellClient().startShell(
        { connId: this.connId, scope: { database: this.scope.database, schema: this.scope.schema }, cols: this.term.cols, rows: this.term.rows, password },
        { signal: ctrl.signal },
      )
      for await (const msg of stream) {
        const ev = msg.event
        if (ev.case === 'started') {
          this.#shellId = ev.value.shellId
          this.display = ev.value.display
          this.state = 'running'
          this.term.focus()
        } else if (ev.case === 'output') {
          this.term.write(ev.value)
        } else if (ev.case === 'exited') {
          this.state = 'exited'
          this.status = ev.value.error || `exited with code ${ev.value.code}`
        }
      }
      if (this.state !== 'exited') {
        this.state = 'exited'
        this.status = 'the shell ended'
      }
    } catch (err) {
      if (ctrl.signal.aborted || isAbort(err)) return
      if (needsPassword(err) && password === undefined) {
        const pw = await this.#askPassword()
        if (pw !== null) return this.start(pw)
      }
      this.state = 'failed'
      this.status = errorMessage(err)
    } finally {
      if (this.#controller === ctrl) this.#shellId = ''
    }
  }

  #input(data: Uint8Array) {
    if (!this.#shellId) return
    shellClient()
      .shellInput({ shellId: this.#shellId, data })
      .catch(() => {})
  }

  // dispose kills the CLI (closing the stream does) and frees the terminal.
  dispose() {
    this.#controller?.abort()
    this.#controller = null
    window.removeEventListener('quarry:theme', this.#retheme)
    this.term.dispose()
    this.element.remove()
  }
}
