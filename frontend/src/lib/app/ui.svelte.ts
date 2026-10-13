import type { ResultBuffer } from '../grid/buffer'
import { DEFAULT_LAYOUT, type Layout } from './layout'

export interface CellRef {
  buffer: ResultBuffer
  row: number
  col: number
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
