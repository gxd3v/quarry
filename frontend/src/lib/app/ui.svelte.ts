// Which shell dialogs are open.
class UI {
  // Connection being edited; '' for a new one, null when closed.
  editing = $state<string | null>(null)
  // The new-tab connection picker.
  picking = $state(false)
  // The scope picker of the active tab.
  scoping = $state(false)
}

export const ui = new UI()
