<script lang="ts">
  import type { Snippet } from 'svelte'
  import Icon from './Icon.svelte'

  interface Props {
    title: string
    width?: number
    // Environment that colors the header edge (e.g. prod confirmations).
    env?: string
    onclose: () => void
    children: Snippet
    footer?: Snippet
  }

  let { title, width = 440, env = '', onclose, children, footer }: Props = $props()

  let dialog: HTMLDialogElement

  $effect(() => {
    dialog.showModal()
    return () => dialog.close()
  })

  function oncancel(e: Event) {
    e.preventDefault()
    onclose()
  }

  // A press that starts and ends on the backdrop closes the dialog.
  let downOnBackdrop = false
</script>

<dialog
  bind:this={dialog}
  class="modal"
  class:env={!!env}
  data-env={env || undefined}
  style:width="{width}px"
  {oncancel}
  onpointerdown={(e) => (downOnBackdrop = e.target === dialog)}
  onclick={(e) => {
    if (downOnBackdrop && e.target === dialog) onclose()
  }}
>
  <div class="frame">
    <header>
      <h2>{title}</h2>
      <button class="icon-btn" type="button" title="Close (Esc)" onclick={onclose}>
        <Icon name="close" size={12} />
      </button>
    </header>
    <div class="body">{@render children()}</div>
    {#if footer}
      <footer>{@render footer()}</footer>
    {/if}
  </div>
</dialog>

<style>
  .modal {
    max-width: calc(100vw - 32px);
    max-height: calc(100vh - 48px);
    padding: 0;
    color: var(--text);
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-radius: 8px;
    box-shadow: 0 16px 48px var(--crust);
  }

  .modal.env {
    border-top: 3px solid var(--env);
  }

  .modal::backdrop {
    background: color-mix(in srgb, var(--crust) 60%, transparent);
  }

  .frame {
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 50px);
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 12px 6px 16px;
  }

  h2 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }

  .body {
    padding: 6px 16px 14px;
    overflow: auto;
  }

  footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 8px;
    padding: 10px 16px;
    background: var(--crust);
    border-top: 1px solid var(--surface0);
    border-radius: 0 0 8px 8px;
  }
</style>
