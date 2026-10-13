<script lang="ts">
  import { notify } from '../app/notify.svelte'
  import Icon from '../ui/Icon.svelte'
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each notify.toasts as t (t.id)}
    <div class="toast {t.kind}">
      <span class="text">{t.text}</span>
      <button class="icon-btn" type="button" title="Dismiss" onclick={() => notify.dismiss(t.id)}>
        <Icon name="close" size={11} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: 14px;
    bottom: 34px;
    z-index: 40;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: min(420px, calc(100vw - 28px));
    pointer-events: none;
  }

  .toast {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 8px 6px 8px 12px;
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-left: 3px solid var(--blue);
    border-radius: 6px;
    box-shadow: 0 8px 24px var(--crust);
    pointer-events: auto;
  }

  .toast.error {
    border-left-color: var(--red);
  }

  .toast.success {
    border-left-color: var(--green);
  }

  .text {
    flex: 1;
    padding-top: 3px;
    line-height: 1.4;
    word-break: break-word;
  }
</style>
