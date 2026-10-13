import { mount } from 'svelte'
import './lib/theme/mocha.css'
import './lib/theme/app.css'
import App from './App.svelte'

export default mount(App, { target: document.getElementById('app')! })
