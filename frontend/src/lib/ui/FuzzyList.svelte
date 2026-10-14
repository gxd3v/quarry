<script lang="ts" generics="T">
  import type { Snippet } from 'svelte'
  import { rank, segments } from '../fuzzy'
  import Icon from './Icon.svelte'

  type Segment = { text: string; hit: boolean }

  interface Props {
    items: T[]
    // The text the query is matched against.
    key: (item: T) => string
    // Enter (or a click) picks; e tells about modifiers such as Alt.
    onpick: (item: T, e: KeyboardEvent | MouseEvent) => void
    row: Snippet<[T, Segment[]]>
    query?: string
    placeholder?: string
    limit?: number
    empty?: string
    loading?: boolean
  }

  let { items, key, onpick, row, query = $bindable(''), placeholder = 'Search…', limit = 200, empty = 'Nothing matches', loading = false }: Props = $props()

  let active = $state(0)
  let list = $state<HTMLDivElement>()

  const shown = $derived(rank(items, query, key, limit))

  $effect(() => {
    void shown
    active = 0
  })

  $effect(() => {
    list?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  })

  function onkeydown(e: KeyboardEvent) {
    const n = shown.length
    switch (e.key) {
      case 'ArrowDown':
        active = n ? (active + 1) % n : 0
        break
      case 'ArrowUp':
        active = n ? (active - 1 + n) % n : 0
        break
      case 'PageDown':
        active = Math.min(n - 1, active + 10)
        break
      case 'PageUp':
        active = Math.max(0, active - 10)
        break
      case 'Enter': {
        const it = shown[active]
        if (it) onpick(it.item, e)
        break
      }
      default:
        return
    }
    e.preventDefault()
  }
</script>

<div class="fuzzy">
  <div class="search">
    <Icon name="search" size={15} />
    <!-- svelte-ignore a11y_autofocus -->
    <input type="text" {placeholder} bind:value={query} {onkeydown} autofocus spellcheck="false" />
  </div>
  <div class="list" role="listbox" bind:this={list}>
    {#each shown as r, i (i)}
      <div
        class="item"
        class:active={i === active}
        role="option"
        aria-selected={i === active}
        tabindex="-1"
        data-i={i}
        onpointermove={() => (active = i)}
        onclick={(e) => onpick(r.item, e)}
        onkeydown={() => {}}
      >
        {@render row(r.item, segments(key(r.item), r.positions))}
      </div>
    {:else}
      <div class="empty">
        {#if loading}<span class="spinner"></span>{:else}{empty}{/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .fuzzy {
    display: flex;
    flex-direction: column;
  }

  .search {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 50px;
    padding: 0 16px;
    border-bottom: 1px solid var(--line);
    color: var(--text-3);
  }

  .search input {
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    outline: none;
    color: var(--text-1);
    font-size: 15px;
  }

  .search input::placeholder {
    color: var(--text-3);
  }

  .list {
    height: min(420px, 60vh);
    overflow: auto;
    padding: 6px;
  }

  .item {
    display: flex;
    align-items: center;
    gap: 9px;
    min-height: 32px;
    padding: 0 10px;
    border-radius: var(--radius);
    color: var(--text-1);
    cursor: default;
    white-space: nowrap;
  }

  .item.active {
    background: var(--bg-active);
  }

  .item :global(mark) {
    background: none;
    color: var(--accent);
    font-weight: 600;
  }

  .empty {
    padding: 14px 12px;
    color: var(--text-3);
  }
</style>
