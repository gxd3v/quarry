// User settings (WorkspaceService settings JSON). Keys this frontend does not
// know are kept as they are.
import { workspaceClient } from '../api/clients'
import { cleanOverrides, keymap, type Overrides } from '../keymap'
import { applyTheme, cachedTheme, isTheme, type Theme } from '../theme/themes'

class Settings {
  // Bumped whenever key bindings change, so hints re-render.
  keys = $state(0)
  loaded = $state(false)
  theme = $state<Theme>(cachedTheme())
  #data: Record<string, unknown> = {}

  constructor() {
    keymap.onChange(() => this.keys++)
  }

  async load() {
    const res = await workspaceClient().getSettings({})
    let data: unknown
    try {
      data = JSON.parse(res.json)
    } catch {
      data = {}
    }
    this.#data = data && typeof data === 'object' && !Array.isArray(data) ? (data as Record<string, unknown>) : {}
    keymap.setOverrides(cleanOverrides(this.#data.keymap))
    const theme = isTheme(this.#data.theme) ? this.#data.theme : 'mocha'
    if (theme !== this.theme) {
      this.theme = theme
      applyTheme(theme)
    }
    this.loaded = true
  }

  async setTheme(t: Theme) {
    this.theme = t
    applyTheme(t)
    await this.#save({ ...this.#data, theme: t })
  }

  async #save(data: Record<string, unknown>) {
    await workspaceClient().saveSettings({ json: JSON.stringify(data) })
    this.#data = data
  }

  // setKeymap applies and saves key binding overrides.
  async setKeymap(overrides: Overrides) {
    keymap.setOverrides(overrides)
    const data = { ...this.#data }
    if (Object.keys(overrides).length) data.keymap = { ...overrides }
    else delete data.keymap
    await this.#save(data)
  }
}

export const settings = new Settings()
