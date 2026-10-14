import type { ResultBuffer } from '../grid/buffer'
import type { ScopeLike } from '../sql'
import type { ExportSource } from './exports.svelte'
import { DEFAULT_LAYOUT, type Layout } from './layout'

export interface CellRef {
  buffer: ResultBuffer
  row: number
  col: number
}

// A cell opened in the cell editor (multi-line and JSON values).
export interface CellEditRequest {
  title: string
  type: string
  // null for NULL.
  value: string | null
  readonly: boolean
  onsave: (value: string | null) => void
}

// What the export dialog exports.
export interface ExportRequest {
  connId: string
  scope: ScopeLike
  source: ExportSource
  // Names the file and the export, e.g. "public.orders".
  name: string
}

export interface ConnectionRequest {
  title: string
  resolve: (connId: string | null) => void
}

// Which shell dialogs and panels are open, and the panel layout.
class UI {
  // Connection being edited; '' for a new one, null when closed.
  editing = $state<string | null>(null)
  // The connection picker, while an action waits for a connection.
  pick = $state<ConnectionRequest | null>(null)
  // The scope picker of the active tab.
  scoping = $state(false)
  // The command palette or "go to object".
  palette = $state<'commands' | 'objects' | null>(null)
  shortcuts = $state(false)
  // The cell shown in the cell viewer.
  cell = $state.raw<CellRef | null>(null)
  cellEdit = $state.raw<CellEditRequest | null>(null)
  exporting = $state.raw<ExportRequest | null>(null)
  layout = $state<Layout>({ ...DEFAULT_LAYOUT })

  // pickConnection shows the connection picker; null when dismissed.
  pickConnection(title: string): Promise<string | null> {
    this.pick?.resolve(null)
    return new Promise((resolve) => {
      this.pick = {
        title,
        resolve: (id) => {
          this.pick = null
          resolve(id)
        },
      }
    })
  }
}

export const ui = new UI()
