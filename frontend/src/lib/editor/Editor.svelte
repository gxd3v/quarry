<script lang="ts">
  import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
  import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
  import { PostgreSQL, sql } from '@codemirror/lang-sql'
  import { bracketMatching, indentOnInput } from '@codemirror/language'
  import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
  import { EditorState } from '@codemirror/state'
  import {
    drawSelection,
    dropCursor,
    EditorView,
    highlightActiveLine,
    highlightActiveLineGutter,
    highlightSpecialChars,
    keymap,
    lineNumbers,
  } from '@codemirror/view'
  import { untrack } from 'svelte'
  import { mocha } from './theme'

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
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          drawSelection(),
          dropCursor(),
          indentOnInput(),
          bracketMatching(),
          closeBrackets(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          search({ top: true }),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab]),
          sql({ dialect: PostgreSQL }),
          mocha,
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
