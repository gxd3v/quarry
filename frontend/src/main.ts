import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import { mount } from 'svelte'
import './lib/theme/mocha.css'
import './lib/theme/app.css'
import App from './App.svelte'
import { applyTheme, cachedTheme } from './lib/theme/themes'

applyTheme(cachedTheme())

export default mount(App, { target: document.getElementById('app')! })
