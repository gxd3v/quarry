<script lang="ts">
  import { EditorView } from '@codemirror/view'
  import { untrack } from 'svelte'
  import { editorHost, tabs, type Tab } from '../app/tabs.svelte'
  import { scriptHooks } from '../editor/script'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  let host: HTMLDivElement
  const scrolls = new WeakMap<Tab, ReturnType<EditorView['scrollSnapshot']>>()

  const ANALYZE_MS = 250
  let timer: ReturnType<typeof setTimeout> | undefined

  $effect(() => {
    const first = untrack(() => tab)
    const view = new EditorView({ parent: host, state: first.editor })
    editorHost.view = view
    editorHost.shown = first
    scriptHooks.onUpdate = (u) => {
      const t = editorHost.shown
      if (!t || (!u.docChanged && !u.selectionSet)) return
      // Cursor moves reuse the last analysis; edits ask the server again.
      if (!u.docChanged) tabs.highlight(t)
      clearTimeout(timer)
      timer = setTimeout(() => {
        if (editorHost.shown === t) void tabs.analyze(t)
      }, ANALYZE_MS)
    }
    void tabs.analyze(first)
    return () => {
      clearTimeout(timer)
      scriptHooks.onUpdate = undefined
      if (editorHost.shown) editorHost.shown.editor = view.state
      editorHost.view = null
      editorHost.shown = null
      view.destroy()
    }
  })

  // Swap the tab's state into the view, keeping each tab's scroll position.
  $effect(() => {
    const next = tab
    const view = untrack(() => editorHost.view)
    if (!view || editorHost.shown === next) return
    const prev = editorHost.shown
    if (prev) {
      prev.editor = view.state
      scrolls.set(prev, view.scrollSnapshot())
    }
    editorHost.shown = next
    view.setState(next.editor)
    const snap = scrolls.get(next)
    if (snap) view.dispatch({ effects: snap })
    view.focus()
    void tabs.analyze(next)
  })
</script>

<div class="editor" bind:this={host}></div>

<style>
  .editor {
    height: 100%;
    overflow: hidden;
  }
</style>
