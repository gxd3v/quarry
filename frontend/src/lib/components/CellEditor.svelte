<script lang="ts">
  import type { CellEditRequest } from '../app/ui.svelte'
  import Modal from '../ui/Modal.svelte'
  import { prettyJson } from '../viewer/format'

  interface Props {
    request: CellEditRequest
    onclose: () => void
  }

  let { request, onclose }: Props = $props()

  // svelte-ignore state_referenced_locally (the request is fixed while open)
  let text = $state(request.value ?? '')
  // svelte-ignore state_referenced_locally
  let isNull = $state(request.value === null)

  const pretty = $derived(isNull ? null : prettyJson(text))

  function save() {
    request.onsave(isNull ? null : text)
    onclose()
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      if (!request.readonly) save()
    }
  }
</script>

<Modal title={request.title} width={720} {onclose}>
  <div class="bar">
    <span class="type">{request.type}</span>
    <span class="size">{isNull ? 'NULL' : `${[...text].length.toLocaleString('en-US')} characters`}</span>
    <span class="spacer"></span>
    {#if !request.readonly}
      <button class="btn small" type="button" disabled={pretty === null || pretty === text} onclick={() => (text = pretty ?? text)}>Format JSON</button>
      <label class="null-toggle"><input type="checkbox" bind:checked={isNull} /> NULL</label>
    {/if}
  </div>
  <!-- svelte-ignore a11y_autofocus -->
  <textarea
    class="value"
    spellcheck="false"
    readonly={request.readonly}
    disabled={isNull}
    placeholder={isNull ? 'NULL' : ''}
    bind:value={text}
    {onkeydown}
    autofocus
  ></textarea>
  {#snippet footer()}
    <span class="hint">Ctrl+Enter saves</span>
    <button class="btn" type="button" onclick={onclose}>{request.readonly ? 'Close' : 'Cancel'}</button>
    {#if !request.readonly}
      <button class="btn primary" type="button" onclick={save}>Set value</button>
    {/if}
  {/snippet}
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

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }

  .null-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--subtext0);
  }

  .value {
    display: block;
    width: 100%;
    min-height: 260px;
    max-height: 60vh;
    padding: 10px 12px;
    resize: vertical;
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 5px;
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    outline: none;
  }

  .value:focus {
    border-color: var(--accent);
  }

  .value:disabled {
    color: var(--overlay0);
  }

  .hint {
    margin-right: auto;
    color: var(--overlay1);
    font-size: 12px;
  }
</style>
