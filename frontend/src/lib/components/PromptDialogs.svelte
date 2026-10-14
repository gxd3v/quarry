<script lang="ts">
  import { prompts } from '../app/prompts.svelte'
  import Modal from '../ui/Modal.svelte'

  let password = $state('')
  let input = $state('')
  let params = $state<Record<string, string>>({})

  $effect(() => {
    if (prompts.password) password = ''
  })

  $effect(() => {
    if (prompts.input) input = prompts.input.value
  })

  $effect(() => {
    const req = prompts.params
    if (req) params = Object.fromEntries(req.fields.map((f) => [f.name, req.values[f.name] ?? '']))
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

{#if prompts.review}
  {@const req = prompts.review}
  <Modal title={req.title} width={560} env={req.env} onclose={() => req.resolve(false)}>
    {#if req.env === 'prod'}<p class="prod">PRODUCTION</p>{/if}
    {#if req.text}<p class="text">{req.text}</p>{/if}
    <ul class="review">
      {#each req.items as it, i (i)}
        <li>
          <span class="verb" class:danger={req.env === 'prod'}>{it.title}</span>
          {#if it.detail}<span class="detail">{it.detail}</span>{/if}
          {#if it.code}<code>{it.code}</code>{/if}
        </li>
      {/each}
    </ul>
    {#snippet footer()}
      <!-- svelte-ignore a11y_autofocus -->
      <button class="btn" type="button" onclick={() => req.resolve(false)} autofocus>Cancel</button>
      <button class="btn danger solid" type="button" data-confirm onclick={() => req.resolve(true)}>{req.confirm}</button>
    {/snippet}
  </Modal>
{/if}

{#if prompts.params}
  {@const req = prompts.params}
  <Modal title={req.title} width={460} onclose={() => req.resolve(null)}>
    <form
      id="params-form"
      class="params"
      onsubmit={(e) => {
        e.preventDefault()
        req.resolve({ ...params })
      }}
    >
      {#each req.fields as f, i (f.name)}
        <label class="param" for="param-{i}">{f.label}</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="param-{i}" class="field mono" type="text" data-param={f.name} bind:value={params[f.name]} autofocus={i === 0} />
      {/each}
    </form>
    <p class="hint">Numbers are inserted as numbers and other values as quoted strings. <code>NULL</code> is NULL; a value in single quotes is always a string.</p>
    {#snippet footer()}
      <button class="btn" type="button" onclick={() => req.resolve(null)}>Cancel</button>
      <button class="btn primary" type="submit" form="params-form">Run</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .review {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
  }

  .review li {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 2px 10px;
    align-items: baseline;
    padding: 6px 8px;
    background: var(--bg-field);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }

  .verb {
    color: var(--peach);
    font-weight: 700;
    font-size: 12px;
    white-space: nowrap;
  }

  .verb.danger {
    color: var(--red);
  }

  .detail {
    color: var(--text-2);
  }

  .review code {
    grid-column: 1 / -1;
    color: var(--subtext1);
    font-family: var(--font-mono);
    font-size: 12px;
    word-break: break-word;
  }

  .params {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 8px 12px;
    align-items: center;
  }

  .param {
    color: var(--maroon);
    font-family: var(--font-mono);
  }

  .mono {
    font-family: var(--font-mono);
  }

  .hint code {
    color: var(--subtext1);
  }

  .label {
    display: block;
    margin-bottom: 6px;
    color: var(--text-2);
    font-size: 12px;
  }

  .error {
    margin: 8px 0 0;
    color: var(--red);
  }

  .hint {
    margin: 8px 0 0;
    color: var(--text-3);
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
    border-radius: var(--radius);
    background: var(--env-prod);
    color: var(--crust);
    font-weight: 700;
    letter-spacing: 0.08em;
    font-size: 11px;
  }
</style>
