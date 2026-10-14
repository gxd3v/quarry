<script lang="ts">
  import type { Snippet } from 'svelte'
  import Icon from './Icon.svelte'

  interface Props {
    title: string
    width?: number
    // Environment that colors the header edge (e.g. prod confirmations).
    env?: string
    onclose: () => void
    // A search surface: no header, near the top of the window.
    bare?: boolean
    children: Snippet
    footer?: Snippet
  }

  let { title, width = 440, env = '', onclose, bare = false, children, footer }: Props = $props()

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
  class:bare
  aria-label={title}
  data-env={env || undefined}
  style:width="{width}px"
  {oncancel}
  onpointerdown={(e) => (downOnBackdrop = e.target === dialog)}
  onclick={(e) => {
    if (downOnBackdrop && e.target === dialog) onclose()
  }}
>
  <div class="frame">
    {#if !bare}
      <header>
        <h2>{title}</h2>
        <button class="icon-btn" type="button" title="Close (Esc)" onclick={onclose}>
          <Icon name="close" size={12} />
        </button>
      </header>
    {/if}
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
    color: var(--text-1);
    background: var(--bg-main);
    border: 1px solid var(--line-strong);
    border-radius: 12px;
    box-shadow:
      0 24px 64px rgb(0 0 0 / 0.5),
      0 2px 8px rgb(0 0 0 / 0.25);
    overflow: hidden;
  }

  .modal[open] {
    animation: pop 0.14s ease-out;
  }

  .modal.bare {
    margin-top: 12vh;
  }

  .modal.bare .body {
    padding: 0;
  }

  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(6px) scale(0.985);
    }
  }

  .modal.env::before {
    content: '';
    position: absolute;
    inset: 0 0 auto;
    height: 3px;
    background: var(--env);
  }

  .modal::backdrop {
    background: rgb(0 0 0 / 0.45);
    backdrop-filter: blur(2px);
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
    padding: 16px 14px 6px 20px;
  }

  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }

  .body {
    padding: 8px 20px 18px;
    overflow: auto;
  }

  footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 8px;
    padding: 12px 20px;
    border-top: 1px solid var(--line);
    background: color-mix(in srgb, var(--crust) 30%, var(--bg-main));
  }
</style>
