// Catppuccin flavors (U-12). mocha.css holds the defaults; the others
// override the palette variables on the root element.

export const THEMES = ['mocha', 'macchiato', 'frappe', 'latte'] as const
export type Theme = (typeof THEMES)[number]

export const THEME_LABELS: Record<Theme, string> = {
  mocha: 'Catppuccin Mocha',
  macchiato: 'Catppuccin Macchiato',
  frappe: 'Catppuccin Frappé',
  latte: 'Catppuccin Latte (light)',
}

const NAMES = [
  'rosewater', 'flamingo', 'pink', 'mauve', 'red', 'maroon', 'peach', 'yellow', 'green', 'teal', 'sky', 'sapphire', 'blue', 'lavender',
  'text', 'subtext1', 'subtext0', 'overlay2', 'overlay1', 'overlay0', 'surface2', 'surface1', 'surface0', 'base', 'mantle', 'crust',
] as const

// Colors in NAMES order, from the Catppuccin palette.
const PALETTES: Record<Exclude<Theme, 'mocha'>, readonly string[]> = {
  macchiato: [
    '#f4dbd6', '#f0c6c6', '#f5bde6', '#c6a0f6', '#ed8796', '#ee99a0', '#f5a97f', '#eed49f', '#a6da95', '#8bd5ca', '#91d7e3', '#7dc4e4', '#8aadf4', '#b7bdf8',
    '#cad3f5', '#b8c0e0', '#a5adcb', '#939ab7', '#8087a2', '#6e738d', '#5b6078', '#494d64', '#363a4f', '#24273a', '#1e2030', '#181926',
  ],
  frappe: [
    '#f2d5cf', '#eebebe', '#f4b8e4', '#ca9ee6', '#e78284', '#ea999c', '#ef9f76', '#e5c890', '#a6d189', '#81c8be', '#99d1db', '#85c1dc', '#8caaee', '#babbf1',
    '#c6d0f5', '#b5bfe2', '#a5adce', '#949cbb', '#838ba7', '#737994', '#626880', '#51576d', '#414559', '#303446', '#292c3c', '#232634',
  ],
  latte: [
    '#dc8a78', '#dd7878', '#ea76cb', '#8839ef', '#d20f39', '#e64553', '#fe640b', '#df8e1d', '#40a02b', '#179299', '#04a5e5', '#209fb5', '#1e66f5', '#7287fd',
    '#4c4f69', '#5c5f77', '#6c6f85', '#7c7f93', '#8c8fa1', '#9ca0b0', '#acb0be', '#bcc0cc', '#ccd0da', '#eff1f5', '#e6e9ef', '#dce0e8',
  ],
}

export function isTheme(v: unknown): v is Theme {
  return typeof v === 'string' && (THEMES as readonly string[]).includes(v)
}

// themeVars returns the CSS variables a theme sets; mocha sets none.
export function themeVars(t: Theme): Record<string, string> {
  if (t === 'mocha') return {}
  const colors = PALETTES[t]
  return Object.fromEntries(NAMES.map((n, i) => [`--${n}`, colors[i]]))
}

const CACHE_KEY = 'quarry.theme'

// applyTheme sets the theme on the document and tells listeners (terminals)
// that colors changed. The choice is cached so the next start has no flash.
export function applyTheme(t: Theme, root: HTMLElement = document.documentElement) {
  for (const n of NAMES) root.style.removeProperty(`--${n}`)
  for (const [k, v] of Object.entries(themeVars(t))) root.style.setProperty(k, v)
  root.style.colorScheme = t === 'latte' ? 'light' : 'dark'
  root.dataset.theme = t
  try {
    localStorage.setItem(CACHE_KEY, t)
  } catch {
    // storage may be unavailable
  }
  window.dispatchEvent(new Event('quarry:theme'))
}

export function cachedTheme(): Theme {
  try {
    const v = localStorage.getItem(CACHE_KEY)
    return isTheme(v) ? v : 'mocha'
  } catch {
    return 'mocha'
  }
}
