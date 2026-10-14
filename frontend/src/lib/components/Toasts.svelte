<script lang="ts">
  import { notify } from '../app/notify.svelte'
  import Icon from '../ui/Icon.svelte'
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each notify.toasts as t (t.id)}
    <div class="toast {t.kind}">
      <span class="mark"></span>
      <span class="text">
        {t.text}
        {#if t.actions.length}
          <span class="actions">
            {#each t.actions as a (a.label)}
              <button class="btn small" type="button" onclick={() => a.run()}>{a.label}</button>
            {/each}
          </span>
        {/if}
      </span>
      <button class="icon-btn" type="button" title="Dismiss" onclick={() => notify.dismiss(t.id)}>
        <Icon name="close" size={11} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: 16px;
    bottom: 38px;
    z-index: 40;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: min(400px, calc(100vw - 32px));
    pointer-events: none;
  }

  .toast {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 8px 10px 12px;
    background: var(--bg-main);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius-l);
    box-shadow: var(--shadow-pop);
    pointer-events: auto;
    animation: in 0.18s ease-out;
  }

  @keyframes in {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
  }

  .mark {
    flex: none;
    width: 8px;
    height: 8px;
    margin-top: 6px;
    border-radius: 50%;
    background: var(--blue);
  }

  .toast.error .mark {
    background: var(--red);
  }

  .toast.success .mark {
    background: var(--green);
  }

  .actions {
    display: flex;
    gap: 6px;
    margin-top: 8px;
  }

  .text {
    flex: 1;
    padding-top: 2px;
    line-height: 1.45;
    word-break: break-word;
  }
</style>
