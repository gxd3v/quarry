<script lang="ts">
  import { untrack } from 'svelte'
  import { key, summary, type DocNode } from '../docs/tree'
  import JsonTree from './JsonTree.svelte'

  interface Props {
    node: DocNode
    label?: string
    // Nesting levels opened at first.
    open?: number
  }

  let { node, label, open = 1 }: Props = $props()
  // Starts open for the first levels; then it is the user's.
  let expanded = $state(untrack(() => open > 0))
  const container = $derived(node.kind !== 'scalar')
  const size = $derived(node.kind === 'object' ? node.entries.length : node.kind === 'array' ? node.items.length : 0)
</script>

<div class="node">
  {#if container}
    <button class="line" type="button" onclick={() => (expanded = !expanded)}>
      <span class="twisty" class:open={expanded}>▸</span>
      {#if label !== undefined}<span class="key">{label}</span><span class="punct">: </span>{/if}
      {#if expanded}
        <span class="punct">{node.kind === 'array' ? '[' : '{'}</span><span class="size">{size} {node.kind === 'array' ? (size === 1 ? 'item' : 'items') : size === 1 ? 'field' : 'fields'}</span>
      {:else}
        <span class="collapsed">{summary(node, 100)}</span>
      {/if}
    </button>
    {#if expanded}
      <div class="children">
        {#if node.kind === 'object'}
          {#each node.entries as [k, v], i (i)}<JsonTree node={v} label={key(k)} open={open - 1} />{/each}
        {:else if node.kind === 'array'}
          {#each node.items as v, i (i)}<JsonTree node={v} label={String(i)} open={open - 1} />{/each}
        {/if}
      </div>
      <div class="punct close">{node.kind === 'array' ? ']' : '}'}</div>
    {/if}
  {:else if node.kind === 'scalar'}
    <div class="line leaf">
      {#if label !== undefined}<span class="key">{label}</span><span class="punct">: </span>{/if}
      <span class="v {node.type}">{node.text}</span>
    </div>
  {/if}
</div>

<style>
  .node {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 20px;
  }

  .line {
    display: flex;
    align-items: baseline;
    gap: 0;
    width: 100%;
    padding: 0;
    border: 0;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    white-space: nowrap;
    overflow: hidden;
  }

  button.line {
    cursor: pointer;
  }

  button.line:hover {
    background: color-mix(in srgb, var(--surface0) 60%, transparent);
  }

  .leaf {
    padding-left: 14px;
  }

  .twisty {
    display: inline-block;
    width: 14px;
    color: var(--muted);
    transition: transform 0.1s;
  }

  .twisty.open {
    transform: rotate(90deg);
  }

  .children {
    padding-left: 16px;
    border-left: 1px solid var(--line);
    margin-left: 6px;
  }

  .close {
    padding-left: 14px;
  }

  .key {
    color: var(--blue);
  }

  .punct,
  .size {
    color: var(--overlay2);
  }

  /* Flex items drop trailing spaces. */
  .key + .punct {
    margin-right: 0.6ch;
  }

  .size {
    margin-left: 6px;
    font-style: italic;
  }

  .collapsed {
    color: var(--text-2);
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .v {
    white-space: pre-wrap;
    word-break: break-all;
  }

  .v.string {
    color: var(--green);
  }

  .v.number {
    color: var(--peach);
  }

  .v.bool,
  .v.null {
    color: var(--mauve);
  }

  .v.special {
    color: var(--teal);
  }
</style>
