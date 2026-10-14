<script lang="ts">
  import { idOf, parseDoc } from '../docs/tree'
  import type { TableSource } from '../table/source'
  import Icon from '../ui/Icon.svelte'
  import JsonTree from './JsonTree.svelte'

  interface Props {
    source: TableSource
    // Column holding the whole document.
    col: number
    // Bumps when rows change.
    version: number
    editable: boolean
    onedit: (row: number) => void
    ondelete: (row: number) => void
  }

  let { source, col, version, editable, onedit, ondelete }: Props = $props()

  // Documents render lazily: a page at a time.
  const PAGE = 50
  let shown = $state(PAGE)

  const docs = $derived.by(() => {
    void version
    const n = Math.min(source.length, shown)
    return Array.from({ length: n }, (_, row) => {
      const tree = parseDoc(source.value(row, col))
      return { row, tree, id: idOf(tree), state: source.state(row) || (source.edited(row, col) ? 'edited' : '') }
    })
  })
  const total = $derived.by(() => {
    void version
    return source.length
  })
</script>

<div class="docs">
  {#each docs as d (d.row)}
    <article class="doc {d.state}">
      <header>
        <span class="n">{d.row + 1}</span>
        <code class="id">{d.id || '(no _id)'}</code>
        <span class="spacer"></span>
        {#if editable && d.state !== 'deleted'}
          <button class="icon-btn" type="button" title="Edit document" onclick={() => onedit(d.row)}><Icon name="edit" size={12} /></button>
          <button class="icon-btn" type="button" title="Delete document" onclick={() => ondelete(d.row)}><Icon name="trash" size={12} /></button>
        {/if}
      </header>
      {#if d.tree}
        <JsonTree node={d.tree} open={1} />
      {:else}
        <p class="raw">{source.value(d.row, col) ?? ''}</p>
      {/if}
    </article>
  {/each}
  {#if total > shown}
    <button class="btn more" type="button" onclick={() => (shown += PAGE)}>Show {Math.min(PAGE, total - shown)} more of {total - shown}</button>
  {/if}
</div>

<style>
  .docs {
    height: 100%;
    overflow: auto;
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .doc {
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 4px 10px 6px;
    background: var(--bg-side);
  }

  .doc.new {
    border-color: color-mix(in srgb, var(--green) 50%, transparent);
  }

  .doc.edited {
    border-color: color-mix(in srgb, var(--peach) 50%, transparent);
  }

  .doc.deleted {
    opacity: 0.5;
    border-color: color-mix(in srgb, var(--red) 50%, transparent);
  }

  header {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
  }

  .n {
    color: var(--faint);
    font-size: 11px;
  }

  .id {
    color: var(--teal);
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .spacer {
    flex: 1;
  }

  .raw {
    margin: 0;
    font-family: var(--font-mono);
    white-space: pre-wrap;
  }

  .more {
    align-self: center;
  }
</style>
