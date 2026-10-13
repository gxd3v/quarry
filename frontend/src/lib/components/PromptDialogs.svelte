<script lang="ts">
  import { prompts } from '../app/prompts.svelte'
  import Modal from '../ui/Modal.svelte'

  let password = $state('')

  $effect(() => {
    if (prompts.password) password = ''
  })
</script>

{#if prompts.password}
  {@const req = prompts.password}
  <Modal title={req.title} width={380} onclose={() => req.resolve(null)}>
    <form
      id="password-form"
      onsubmit={(e) => {
        e.preventDefault()
        req.resolve(password)
      }}
    >
      <!-- svelte-ignore a11y_autofocus -->
      <input class="field" type="password" autocomplete="off" placeholder="Password" bind:value={password} autofocus />
      {#if req.error}<p class="error">{req.error}</p>{/if}
      <p class="hint">Used for this session only.</p>
    </form>
    {#snippet footer()}
      <button class="btn" type="button" onclick={() => req.resolve(null)}>Cancel</button>
      <button class="btn primary" type="submit" form="password-form">Connect</button>
    {/snippet}
  </Modal>
{/if}

{#if prompts.confirm}
  {@const req = prompts.confirm}
  <Modal title={req.title} width={440} env={req.env} onclose={() => req.resolve(false)}>
    {#if req.env === 'prod'}<p class="prod">PRODUCTION</p>{/if}
    <p class="text">{req.text}</p>
    {#snippet footer()}
      <!-- svelte-ignore a11y_autofocus -->
      <button class="btn" type="button" onclick={() => req.resolve(false)} autofocus>Cancel</button>
      <button class="btn" class:danger={req.danger} class:solid={req.danger} type="button" onclick={() => req.resolve(true)}>
        {req.confirm}
      </button>
    {/snippet}
  </Modal>
{/if}

<style>
  .error {
    margin: 8px 0 0;
    color: var(--red);
  }

  .hint {
    margin: 8px 0 0;
    color: var(--overlay1);
    font-size: 12px;
  }

  .text {
    margin: 4px 0;
    line-height: 1.5;
    white-space: pre-wrap;
  }

  .prod {
    display: inline-block;
    margin: 0 0 8px;
    padding: 2px 8px;
    border-radius: 4px;
    background: var(--env-prod);
    color: var(--crust);
    font-weight: 700;
    letter-spacing: 0.08em;
    font-size: 11px;
  }
</style>
