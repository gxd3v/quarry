<script lang="ts">
  import { connectionClient } from '../api/clients'
  import { errorMessage } from '../api/errors'
  import { deleteConnection, groupNames } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import {
    applyParsed,
    configFromForm,
    defaultName,
    emptyForm,
    ENVS,
    formFromConfig,
    passwordForSave,
    passwordForTest,
    SSL_MODES,
    validate,
    type ConnForm,
  } from '../connections/form'
  import { sshConfigured, sshFlag, sshSecretsForSave, withSshFlag, withoutSsh, type SshFlag } from '../connections/ssh'
  import DriverBadge, { engineName } from '../ui/DriverBadge.svelte'
  import Icon from '../ui/Icon.svelte'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    // Connection to edit, '' for a new one.
    id: string
    onclose: () => void
  }

  let { id, onclose }: Props = $props()

  function initial(): ConnForm {
    const c = connections.get(id)
    if (c?.config) return formFromConfig(c.config, c.hasPassword)
    return emptyForm(connections.drivers[0]?.kind ?? 'postgres')
  }

  let form = $state(initial())
  let uri = $state('')
  let error = $state('')
  let test = $state<{ ok: boolean; text: string } | null>(null)
  let busy = $state<'' | 'test' | 'save' | 'parse'>('')

  const driver = $derived(connections.drivers.find((d) => d.kind === form.kind))
  const kinds = $derived(connections.drivers.length ? connections.drivers.map((d) => d.kind) : ['postgres'])
  const groups = $derived(groupNames())
  const ENV_LABELS: Record<string, string> = { local: 'Local', dev: 'Dev', qa: 'QA', prod: 'Production' }
  const editing = $derived(id !== '')

  async function parse() {
    if (!uri.trim()) return
    busy = 'parse'
    error = ''
    try {
      form = applyParsed(form, await connectionClient().parseURI({ uri: uri.trim() }))
      uri = ''
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function runTest() {
    const problem = validate({ ...form, name: form.name || 'test' })
    if (problem) {
      error = problem
      return
    }
    busy = 'test'
    error = ''
    test = null
    try {
      const res = await connectionClient().testConnection({ config: configFromForm(form), password: passwordForTest(form) })
      test = { ok: true, text: `Connected in ${res.latencyMs} ms` }
    } catch (err) {
      test = { ok: false, text: errorMessage(err) }
    } finally {
      busy = ''
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    const problem = validate(form)
    if (problem) {
      error = problem
      return
    }
    busy = 'save'
    error = ''
    try {
      const saved = await connections.save(configFromForm(form), { ...passwordForSave(form), ...sshSecretsForSave(form) })
      notify.flash(`Saved ${saved.config!.name}`)
      onclose()
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function remove() {
    if (await deleteConnection(id)) onclose()
  }

  function option(key: string, value: string) {
    form.options = { ...form.options, [key]: value }
  }

  // Opened for connections that already have a tunnel.
  let sshOpen = $state(sshConfigured(initial().options))

  function flag(key: SshFlag, on: boolean) {
    form.options = withSshFlag(form.options, key, on)
  }

  function removeSsh() {
    form.options = withoutSsh(form.options)
    form.sshPassword = form.sshPassphrase = ''
    form.sshPasswordTouched = form.sshPassphraseTouched = true
  }
</script>

<Modal title={editing ? `Edit ${form.name || 'connection'}` : 'New connection'} width={600} env={form.env} {onclose}>
  <form id="conn-form" onsubmit={save} autocomplete="off">
    <div class="uri">
      <Icon name="link" size={13} />
      <input
        type="text"
        placeholder="Paste a connection URI to fill the form, e.g. postgres://user@host:5432/db"
        bind:value={uri}
        onpaste={() => setTimeout(parse)}
        onkeydown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            parse()
          }
        }}
      />
      {#if uri.trim()}<button class="btn small" type="button" disabled={busy !== ''} onclick={parse}>Fill</button>{/if}
    </div>

    <div class="engines" role="radiogroup" aria-label="Database">
      {#each kinds as k (k)}
        <button type="button" role="radio" class="engine" class:on={form.kind === k} aria-checked={form.kind === k} onclick={() => (form.kind = k)}>
          <DriverBadge kind={k} active={form.kind === k} size={22} />
          <span>{engineName(k)}</span>
        </button>
      {/each}
    </div>

    <div class="grid">
      <label class="wide">
        <span>Name</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="field" type="text" bind:value={form.name} placeholder={defaultName(form) || 'e.g. Billing'} autofocus />
      </label>
      <label class="wide">
        <span>Folder</span>
        <input class="field" type="text" list="conn-groups" bind:value={form.group} placeholder="None · nested with /, e.g. Acme/PRD" />
        <datalist id="conn-groups">
          {#each groups as g (g)}<option value={g}></option>{/each}
        </datalist>
      </label>

      <div class="wide env-field">
        <span>Environment</span>
        <div class="envs" role="radiogroup" aria-label="Environment">
          {#each ENVS as env (env)}
            <button type="button" role="radio" aria-checked={form.env === env} class="env" class:on={form.env === env} data-env={env} onclick={() => (form.env = env)}>
              <span class="env-dot" data-env={env}></span>{ENV_LABELS[env]}
            </button>
          {/each}
        </div>
      </div>
    </div>

    <h3>Server</h3>
    <div class="grid three">
      <label class="span2">
        <span>Host</span>
        <input class="field" type="text" bind:value={form.host} />
      </label>
      <label>
        <span>Port</span>
        <input class="field" type="text" inputmode="numeric" placeholder={String(driver?.defaultPort ?? '')} bind:value={form.port} />
      </label>
      <label class="span2">
        <span>{form.kind === 'oracle' ? 'Service name' : form.kind === 'redis' ? 'Database index' : 'Database'}</span>
        <input class="field" type="text" bind:value={form.database} placeholder={form.kind === 'redis' ? '0' : ''} />
      </label>
      {#if form.kind === 'postgres'}
        <label>
          <span>SSL mode</span>
          <select class="field" value={form.options.sslmode ?? ''} onchange={(e) => option('sslmode', e.currentTarget.value)}>
            {#each SSL_MODES as m (m)}<option value={m}>{m || 'prefer'}</option>{/each}
          </select>
        </label>
      {:else if form.kind === 'oracle'}
        <label>
          <span>SID</span>
          <input class="field" type="text" placeholder="instead of service" value={form.options.sid ?? ''} oninput={(e) => option('sid', e.currentTarget.value)} />
        </label>
      {/if}
    </div>

    <h3>Authentication</h3>
    <div class="grid">
      <label>
        <span>User</span>
        <input class="field" type="text" bind:value={form.user} />
      </label>
      <label>
        <span>Password</span>
        <input
          class="field"
          type="password"
          disabled={form.passwordMode === 'ask'}
          placeholder={form.passwordMode === 'ask' ? 'Asked on connect' : form.hasPassword && !form.passwordTouched ? '••••••••  saved' : ''}
          bind:value={form.password}
          oninput={() => (form.passwordTouched = true)}
        />
      </label>
      <div class="wide inline">
        <div class="seg" role="radiogroup" aria-label="Password storage">
          <button type="button" role="radio" aria-checked={form.passwordMode === 'keyring'} class:on={form.passwordMode === 'keyring'} onclick={() => (form.passwordMode = 'keyring')}>Save in keyring</button>
          <button type="button" role="radio" aria-checked={form.passwordMode === 'ask'} class:on={form.passwordMode === 'ask'} onclick={() => (form.passwordMode = 'ask')}>Ask every time</button>
        </div>
        <span class="spacer"></span>
        <label class="check"><input type="checkbox" bind:checked={form.readOnly} /> Read-only</label>
        <label class="check"><input type="checkbox" bind:checked={form.favorite} /> Favorite</label>
      </div>
    </div>

    <details class="ssh" bind:open={sshOpen}>
      <summary>SSH tunnel{#if form.options.ssh_host}<span class="hint">{form.options.ssh_host}</span>{/if}</summary>
      <div class="grid three">
        <label class="span2">
          <span>SSH host</span>
          <input class="field" type="text" placeholder="bastion.example.com" value={form.options.ssh_host ?? ''} oninput={(e) => option('ssh_host', e.currentTarget.value)} />
        </label>
        <label>
          <span>Port</span>
          <input class="field" type="text" inputmode="numeric" placeholder="22" value={form.options.ssh_port ?? ''} oninput={(e) => option('ssh_port', e.currentTarget.value)} />
        </label>
      </div>
      <div class="grid">
        <label>
          <span>SSH user</span>
          <input class="field" type="text" placeholder="Current user" value={form.options.ssh_user ?? ''} oninput={(e) => option('ssh_user', e.currentTarget.value)} />
        </label>
        <label>
          <span>SSH password</span>
          <input class="field" type="password" placeholder={form.sshPasswordTouched ? '' : 'Keyring, if saved'} bind:value={form.sshPassword} oninput={() => (form.sshPasswordTouched = true)} />
        </label>
        <label>
          <span>Private key</span>
          <input class="field" type="text" placeholder="~/.ssh/id_ed25519" value={form.options.ssh_key ?? ''} oninput={(e) => option('ssh_key', e.currentTarget.value)} />
        </label>
        <label>
          <span>Key passphrase</span>
          <input class="field" type="password" placeholder={form.sshPassphraseTouched ? '' : 'Keyring, if saved'} bind:value={form.sshPassphrase} oninput={() => (form.sshPassphraseTouched = true)} />
        </label>
        <label class="wide">
          <span>Jump hosts</span>
          <input class="field" type="text" placeholder="user@jump1:22, jump2" value={form.options.ssh_jump ?? ''} oninput={(e) => option('ssh_jump', e.currentTarget.value)} />
        </label>
        <label class="wide">
          <span>known_hosts file</span>
          <input class="field" type="text" placeholder="~/.ssh/known_hosts" value={form.options.ssh_known_hosts ?? ''} oninput={(e) => option('ssh_known_hosts', e.currentTarget.value)} />
        </label>
        <div class="wide inline">
          <label class="check"><input type="checkbox" checked={sshFlag(form.options, 'ssh_agent')} onchange={(e) => flag('ssh_agent', e.currentTarget.checked)} /> Also use the SSH agent</label>
          <label class="check"><input type="checkbox" checked={sshFlag(form.options, 'ssh_accept_new')} onchange={(e) => flag('ssh_accept_new', e.currentTarget.checked)} /> Trust an unknown host key on first connect</label>
          <span class="spacer"></span>
          {#if sshConfigured(form.options)}<button class="btn small ghost danger" type="button" onclick={removeSsh}>Remove tunnel</button>{/if}
        </div>
      </div>
    </details>

    {#if test}
      <p class="note" class:ok={test.ok}><span class="dot"></span>{test.text}</p>
    {/if}
    {#if error}
      <p class="note">{error}</p>
    {/if}
  </form>

  {#snippet footer()}
    {#if editing}
      <button class="btn ghost danger" type="button" onclick={remove}>Delete</button>
    {/if}
    <button class="btn ghost" type="button" disabled={busy !== ''} onclick={runTest}>
      {#if busy === 'test'}<span class="spinner"></span>{/if}Test connection
    </button>
    <span class="spacer"></span>
    <button class="btn" type="button" onclick={onclose}>Cancel</button>
    <button class="btn primary" type="submit" form="conn-form" disabled={busy !== ''}>{editing ? 'Save' : 'Create'}</button>
  {/snippet}
</Modal>

<style>
  form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .uri {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    padding: 0 4px 0 11px;
    background: var(--bg-hover);
    border: 1px dashed var(--line-strong);
    border-radius: var(--radius);
    color: var(--text-3);
  }

  .uri:focus-within {
    border-style: solid;
    border-color: color-mix(in srgb, var(--accent) 60%, transparent);
  }

  .uri input {
    flex: 1;
    min-width: 0;
    background: none;
    border: none;
    outline: none;
    color: var(--text-1);
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .uri input::placeholder {
    color: var(--text-3);
    font-family: var(--font-ui);
    font-size: 13px;
  }

  .engines {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(0, 1fr));
    gap: 8px;
  }

  .engine {
    display: flex;
    align-items: center;
    gap: 9px;
    height: 44px;
    padding: 0 12px;
    background: var(--bg-hover);
    border: 1px solid transparent;
    border-radius: var(--radius-l);
    color: var(--text-2);
    font-weight: 500;
    cursor: pointer;
    transition:
      background 0.12s,
      border-color 0.12s,
      color 0.12s;
  }

  .engine:hover:not(:disabled) {
    background: var(--bg-active);
    color: var(--text-1);
  }

  .engine.on {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
    border-color: color-mix(in srgb, var(--accent) 55%, transparent);
    color: var(--text-1);
  }

  .engine:disabled {
    opacity: 0.4;
    cursor: default;
  }

  h3 {
    margin: 4px 0 -4px;
    color: var(--text-3);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px 12px;
  }

  .grid.three {
    grid-template-columns: 1fr 1fr 1fr;
  }

  .span2 {
    grid-column: span 2;
  }

  .wide {
    grid-column: 1 / -1;
  }

  label,
  .env-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }

  label > span,
  .env-field > span {
    color: var(--text-2);
    font-size: 12px;
    font-weight: 500;
  }

  .envs {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 6px;
  }

  .env {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    height: 30px;
    background: var(--bg-hover);
    border: 1px solid transparent;
    border-radius: var(--radius);
    color: var(--text-2);
    cursor: pointer;
    transition:
      background 0.12s,
      border-color 0.12s;
  }

  .env:hover {
    background: var(--bg-active);
    color: var(--text-1);
  }

  .env.on {
    border-color: color-mix(in srgb, var(--env) 60%, transparent);
    background: color-mix(in srgb, var(--env) 12%, transparent);
    color: var(--text-1);
    font-weight: 500;
  }

  .inline {
    display: flex;
    align-items: center;
    flex-direction: row;
    gap: 16px;
  }

  .seg {
    display: inline-flex;
    padding: 2px;
    border-radius: var(--radius);
    background: var(--bg-hover);
  }

  .seg button {
    height: 24px;
    padding: 0 10px;
    background: none;
    border: none;
    border-radius: var(--radius-s);
    color: var(--text-3);
    font-size: 12px;
    cursor: pointer;
  }

  .seg button:hover {
    color: var(--text-1);
  }

  .seg button.on {
    background: var(--bg-raised);
    color: var(--text-1);
    font-weight: 500;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.25);
  }

  .check {
    flex-direction: row;
    align-items: center;
    gap: 8px;
    color: var(--text-2);
    cursor: pointer;
  }

  .spacer {
    flex: 1;
  }

  .ssh {
    padding-top: 12px;
    border-top: 1px solid var(--line);
  }

  .ssh summary {
    color: var(--text-2);
    font-weight: 500;
  }

  .ssh summary:hover {
    color: var(--text-1);
  }

  .ssh[open] summary {
    margin-bottom: 12px;
  }

  .ssh .grid + .grid {
    margin-top: 12px;
  }

  .hint {
    margin-left: 4px;
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 400;
  }

  .note {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin: 0;
    padding: 9px 12px;
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--red) 10%, transparent);
    color: var(--red);
    white-space: pre-wrap;
    word-break: break-word;
  }

  .note.ok {
    background: color-mix(in srgb, var(--green) 10%, transparent);
    color: var(--green);
  }

  .note .dot {
    flex: none;
    width: 7px;
    height: 7px;
    margin-top: 6px;
    border-radius: 50%;
    background: currentColor;
  }
</style>
