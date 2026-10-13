<script lang="ts">
  import { notify } from '../app/notify.svelte'
  import type { CellRef } from '../app/ui.svelte'
  import { copyText } from '../ui/clipboard'
  import Modal from '../ui/Modal.svelte'
  import { hexDump, jsonTokens, parseHex, prettyJson } from '../viewer/format'

  interface Props {
    cell: CellRef
    onclose: () => void
  }

  let { cell, onclose }: Props = $props()

  type Mode = 'text' | 'json' | 'hex'

  // Above this, JSON is shown without colors to keep the DOM small.
  const MAX_HIGHLIGHT = 200_000

  const col = $derived(cell.buffer.columns[cell.col])
  const display = $derived(cell.buffer.row(cell.row)[cell.col] ?? null)
  const full = $derived(cell.buffer.full(cell.row, cell.col))
  const text = $derived(typeof full === 'string' ? full : full ? null : display)
  const bytes = $derived(full instanceof Uint8Array ? full : col.kind === 'bytes' && display ? parseHex(display) : null)
  const hexText = $derived(bytes ? cell.buffer.text(cell.row, cell.col) : null)
  const pretty = $derived(text !== null && (col.kind === 'json' || /^\s*[[{]/.test(text)) ? prettyJson(text) : null)

  const modes = $derived(
    (['text', 'json', 'hex'] as Mode[]).filter((m) => (m === 'json' ? pretty !== null : m === 'hex' ? bytes !== null : true)),
  )
  let mode = $derived<Mode>(pretty !== null ? 'json' : bytes ? 'hex' : 'text')
  let wrap = $state(true)

  const shown = $derived.by(() => {
    if (mode === 'json' && pretty !== null) return pretty
    if (mode === 'hex' && bytes) return hexDump(bytes)
    return hexText ?? text ?? ''
  })
  const tokens = $derived(mode === 'json' && pretty !== null && pretty.length <= MAX_HIGHLIGHT ? jsonTokens(pretty) : null)

  const size = $derived(
    bytes ? `${bytes.length.toLocaleString('en-US')} bytes` : text !== null ? `${[...text].length.toLocaleString('en-US')} characters` : 'NULL',
  )

  async function copy() {
    try {
      await copyText(mode === 'hex' ? (hexText ?? '') : shown)
      notify.flash('Copied')
    } catch (err) {
      notify.error(err, 'Copy')
    }
  }
</script>

<Modal title="{col.name} · row {cell.row + 1}" width={760} {onclose}>
  <div class="bar">
    <span class="type">{col.type}</span>
    <span class="size">{size}</span>
    <span class="spacer"></span>
    {#if modes.length > 1}
      <div class="modes" role="radiogroup" aria-label="View as">
        {#each modes as m (m)}
          <button type="button" role="radio" aria-checked={mode === m} class:on={mode === m} onclick={() => (mode = m)}>{m.toUpperCase()}</button>
        {/each}
      </div>
    {/if}
    <label class="wrap-toggle"><input type="checkbox" bind:checked={wrap} /> Wrap</label>
    <button class="btn small" type="button" onclick={copy}>Copy</button>
  </div>
  {#if display === null && full === undefined}
    <p class="null">NULL</p>
  {:else}
    <pre class="value" class:wrap={wrap && mode !== 'hex'} tabindex="-1">{#if tokens}{#each tokens as t, i (i)}<span class={t.kind}>{t.text}</span>{/each}{:else}{shown}{/if}</pre>
  {/if}
</Modal>

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 8px;
    font-size: 12px;
  }

  .type {
    color: var(--yellow);
    font-family: var(--font-mono);
  }

  .size {
    color: var(--overlay1);
  }

  .spacer {
    flex: 1;
  }

  .modes {
    display: inline-flex;
    border: 1px solid var(--surface1);
    border-radius: 5px;
    overflow: hidden;
  }

  .modes button {
    height: 22px;
    padding: 0 8px;
    background: none;
    border: none;
    color: var(--subtext0);
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
  }

  .modes button.on {
    background: var(--surface1);
    color: var(--text);
  }

  .wrap-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--subtext0);
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }

  .value {
    margin: 0;
    max-height: min(60vh, 560px);
    min-height: 120px;
    overflow: auto;
    padding: 10px 12px;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    white-space: pre;
    outline: none;
    user-select: text;
  }

  .value.wrap {
    white-space: pre-wrap;
    word-break: break-word;
  }

  .null {
    color: var(--overlay0);
    font-style: italic;
  }

  .value :global(.key) {
    color: var(--blue);
  }

  .value :global(.string) {
    color: var(--green);
  }

  .value :global(.number) {
    color: var(--peach);
  }

  .value :global(.literal) {
    color: var(--mauve);
  }

  .value :global(.punct) {
    color: var(--overlay2);
  }
</style>
