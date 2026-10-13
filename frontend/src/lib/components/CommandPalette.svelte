<script lang="ts">
  import { settings } from '../app/settings.svelte'
  import { keymap, label, type Action } from '../keymap'
  import FuzzyList from '../ui/FuzzyList.svelte'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    onclose: () => void
  }

  let { onclose }: Props = $props()

  const actions = $derived.by(() => {
    void settings.keys
    return keymap
      .actions()
      .filter((a) => a.id !== 'palette.commands')
      .sort((a, b) => name(a).localeCompare(name(b)))
  })

  function name(a: Action): string {
    return a.category ? `${a.category}: ${a.title}` : a.title
  }

  function pick(a: Action) {
    onclose()
    // After the dialog closed, so focus moves and dialog checks behave.
    setTimeout(() => keymap.run(a.id), 0)
  }
</script>

<Modal title="Command palette" width={560} {onclose}>
  <FuzzyList items={actions} key={name} onpick={pick} placeholder="Type a command">
    {#snippet row(a, segs)}
      <span class="label">{#each segs as s, i (i)}{#if s.hit}<mark>{s.text}</mark>{:else}{s.text}{/if}{/each}</span>
      <span class="keys">
        {#each keymap.keysFor(a.id).slice(0, 2) as k (k)}<span class="kbd">{label(k)}</span>{/each}
      </span>
    {/snippet}
  </FuzzyList>
</Modal>

<style>
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .keys {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }

  .kbd {
    padding: 1px 5px;
    border: 1px solid var(--surface1);
    border-radius: 4px;
  }
</style>
