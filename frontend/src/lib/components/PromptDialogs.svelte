<script lang="ts">
  import { prompts } from '../app/prompts.svelte'
  import Modal from '../ui/Modal.svelte'

  let password = $state('')
  let input = $state('')

  $effect(() => {
    if (prompts.password) password = ''
  })

  $effect(() => {
    if (prompts.input) input = prompts.input.value
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

{#if prompts.choice}
  {@const req = prompts.choice}
  <Modal title={req.title} width={440} onclose={() => req.resolve(null)}>
    <p class="text">{req.text}</p>
    {#snippet footer()}
      {#each req.choices as c (c.value)}
        <!-- svelte-ignore a11y_autofocus -->
        <button
          class="btn"
          class:primary={c.primary}
          class:danger={c.danger}
          type="button"
          data-choice={c.value}
          autofocus={c.primary}
          onclick={() => req.resolve(c.value)}>{c.label}</button
        >
      {/each}
    {/snippet}
  </Modal>
{/if}

{#if prompts.input}
  {@const req = prompts.input}
  <Modal title={req.title} width={400} onclose={() => req.resolve(null)}>
    <form
      id="input-form"
      onsubmit={(e) => {
        e.preventDefault()
        req.resolve(input)
      }}
    >
      {#if req.label}<label class="label" for="prompt-input">{req.label}</label>{/if}
      <!-- svelte-ignore a11y_autofocus -->
      <input
        id="prompt-input"
        class="field"
        type="text"
        placeholder={req.placeholder}
        list={req.suggestions.length ? 'prompt-suggestions' : undefined}
        bind:value={input}
        autofocus
      />
      {#if req.suggestions.length}
        <datalist id="prompt-suggestions">
          {#each req.suggestions as s (s)}<option value={s}></option>{/each}
        </datalist>
      {/if}
    </form>
    {#snippet footer()}
      <button class="btn" type="button" onclick={() => req.resolve(null)}>Cancel</button>
      <button class="btn primary" type="submit" form="input-form">{req.confirm}</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .label {
    display: block;
    margin-bottom: 6px;
    color: var(--subtext0);
    font-size: 12px;
  }

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
