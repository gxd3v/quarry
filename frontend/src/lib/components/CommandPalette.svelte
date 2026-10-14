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

  // muteCategory marks the "Category: " prefix of the segments, which show
  // dimmed so the command stands out.
  function muteCategory(a: Action, segs: { text: string; hit: boolean }[]): { text: string; hit: boolean; cat: boolean }[] {
    let left = a.category ? a.category.length + 2 : 0
    const out: { text: string; hit: boolean; cat: boolean }[] = []
    for (const s of segs) {
      if (left <= 0 || s.text.length <= left) {
        out.push({ ...s, cat: left > 0 })
        left -= s.text.length
        continue
      }
      out.push({ text: s.text.slice(0, left), hit: s.hit, cat: true }, { text: s.text.slice(left), hit: s.hit, cat: false })
      left = 0
    }
    return out
  }

  function pick(a: Action) {
    onclose()
    // After the dialog closed, so focus moves and dialog checks behave.
    setTimeout(() => keymap.run(a.id), 0)
  }
</script>

<Modal title="Command palette" width={620} bare {onclose}>
  <FuzzyList items={actions} key={name} onpick={pick} placeholder="Search commands">
    {#snippet row(a, segs)}
      <span class="label">{#each muteCategory(a, segs) as s, i (i)}{#if s.hit}<mark class:cat={s.cat}>{s.text}</mark>{:else}<span class:cat={s.cat}>{s.text}</span>{/if}{/each}</span>
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

  .cat {
    color: var(--text-3);
  }

  .keys {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }


</style>
