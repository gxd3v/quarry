<script lang="ts">
  import { EditorState } from '@codemirror/state'
  import { EditorView } from '@codemirror/view'
  import { untrack } from 'svelte'
  import { baseExtensions } from './setup'

  interface Props {
    // Initial document; later changes to the prop are ignored.
    value: string
    onchange?: (doc: string) => void
  }

  let { value, onchange }: Props = $props()

  let host: HTMLDivElement

  $effect(() => {
    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        // Untracked so a new `value` does not recreate the view.
        doc: untrack(() => value),
        extensions: [
          baseExtensions(),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onchange?.(u.state.doc.toString())
          }),
        ],
      }),
    })
    return () => view.destroy()
  })
</script>

<div class="editor" bind:this={host}></div>

<style>
  .editor {
    height: 100%;
    overflow: hidden;
  }
</style>
