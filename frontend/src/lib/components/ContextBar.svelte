<script lang="ts">
  import { guard, run } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { scopeLabel } from '../sql'
  import DriverBadge from '../ui/DriverBadge.svelte'
  import Icon from '../ui/Icon.svelte'

  interface Props {
    tab: Tab
  }

  let { tab }: Props = $props()

  const cfg = $derived(connections.config(tab.connId))
  const env = $derived(cfg?.env || 'local')
  const scope = $derived(scopeLabel(tab.scope, cfg?.database))

  let now = $state(performance.now())
  $effect(() => {
    if (!tab.busy) return
    const id = setInterval(() => (now = performance.now()), 100)
    return () => clearInterval(id)
  })

  const elapsed = $derived(tab.busy ? Math.max(0, now - tab.startedAt) : 0)

  function fmt(ms: number): string {
    return ms < 10_000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.floor(ms / 60_000)}:${String(Math.floor(ms / 1000) % 60).padStart(2, '0')}`
  }
</script>

<div class="ctx" data-env={env} class:prod={env === 'prod'}>
  <button
    class="target"
    type="button"
    title={tab.kind === 'script' ? 'Change connection or scope' : `${cfg?.name ?? ''}${scope ? ` · ${scope}` : ''}`}
    disabled={tab.kind !== 'script'}
    onclick={() => (ui.scoping = true)}
  >
    <DriverBadge kind={cfg?.kind ?? ''} active={connections.connected(tab.connId)} size={16} />
    <span class="conn">{cfg?.name ?? 'Unknown connection'}</span>
    {#if scope}<span class="sep">/</span><span class="scope">{scope}</span>{/if}
    {#if tab.kind === 'script'}<span class="caret"><Icon name="chevron" size={10} /></span>{/if}
  </button>
  <span class="env-tag" data-env={env}>{env}</span>
  {#if cfg?.readOnly}
    <span class="pill" title="Read-only connection: statements that write are refused"><Icon name="lock" size={11} /> Read-only</span>
  {/if}

  {#if tab.tx.supported && tab.kind !== 'terminal'}
    <div class="seg" role="radiogroup" aria-label="Commit mode" title="Auto-commit commits each statement; manual keeps changes in a transaction until Commit or Rollback">
      <button type="button" role="radio" aria-checked={tab.tx.autoCommit} class:on={tab.tx.autoCommit} disabled={tab.busy !== '' || tab.tx.autoCommit} onclick={() => guard('Change commit mode', () => tabs.setAutoCommit(tab, true))}>Auto</button>
      <button type="button" role="radio" aria-checked={!tab.tx.autoCommit} class:on={!tab.tx.autoCommit} disabled={tab.busy !== '' || !tab.tx.autoCommit} onclick={() => guard('Change commit mode', () => tabs.setAutoCommit(tab, false))}>Manual</button>
    </div>
  {/if}

  {#if tab.tx.inTx}
    <span class="pill tx" title="A transaction is open"><span class="dot"></span>Transaction</span>
    <button class="btn small" type="button" disabled={tab.busy !== ''} onclick={() => guard('Commit', () => tabs.commit(tab))}>Commit</button>
    <button class="btn small ghost danger" type="button" disabled={tab.busy !== ''} onclick={() => guard('Rollback', () => tabs.rollback(tab))}>Rollback</button>
  {/if}

  <span class="spacer"></span>

  {#if tab.busy}
    <span class="elapsed" title={tab.busy === 'fetch' ? 'Fetching' : 'Running'}>
      <span class="spinner"></span>{tab.canceling ? 'Canceling…' : fmt(elapsed)}
    </span>
  {/if}
  {#if tab.kind === 'script'}
    <button class="icon-btn" type="button" title="Format SQL (Alt+Shift+F)" disabled={!!tab.busy} onclick={() => guard('Format SQL', () => tabs.format(tab))}>
      <Icon name="format" size={14} />
    </button>
    <button class="icon-btn" type="button" title="Explain (Ctrl+E)" disabled={!!tab.busy} onclick={() => void tabs.explain(tab, tabs.target(tab, false), false)}>
      <Icon name="plan" size={14} />
    </button>
  {/if}
  {#if tab.busy}
    <button class="btn small stop" type="button" title="Stop (Esc)" onclick={() => tabs.cancel(tab)}>
      <Icon name="stop" size={11} fill /> Stop
    </button>
  {:else if tab.kind === 'table'}
    <button class="btn small" type="button" title="Reload the rows (Ctrl+Enter)" onclick={() => run(tab)}>
      <Icon name="refresh" size={12} /> Refresh
    </button>
  {:else}
    <button class="btn small primary" type="button" title="Run statement or selection (Ctrl+Enter) · whole script (Ctrl+Shift+Enter)" onclick={() => run(tab)}>
      <Icon name="play" size={11} fill /> Run
    </button>
  {/if}
</div>

<style>
  .ctx {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    padding: 0 10px 0 8px;
    background: var(--bg-main);
    border-bottom: 1px solid var(--line);
  }

  .ctx.prod {
    background: color-mix(in srgb, var(--env-prod) 6%, var(--bg-main));
  }

  .target {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
    height: 28px;
    padding: 0 8px 0 6px;
    background: none;
    border: none;
    border-radius: var(--radius);
    color: var(--text-1);
    cursor: pointer;
  }

  .target:disabled {
    cursor: default;
  }

  .target:hover:not(:disabled) {
    background: var(--bg-hover);
  }

  .conn {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sep {
    color: var(--text-3);
  }

  .scope {
    color: var(--text-2);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .caret {
    display: inline-flex;
    color: var(--text-3);
    transform: rotate(90deg);
  }

  .pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex: none;
    height: 22px;
    padding: 0 8px;
    border-radius: 11px;
    background: var(--bg-hover);
    color: var(--text-2);
    font-size: 12px;
  }

  .pill.tx {
    background: color-mix(in srgb, var(--peach) 14%, transparent);
    color: var(--peach);
  }

  .pill .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: currentColor;
  }

  .seg {
    display: inline-flex;
    flex: none;
    padding: 2px;
    border-radius: var(--radius);
    background: var(--bg-hover);
  }

  .seg button {
    height: 22px;
    padding: 0 9px;
    background: none;
    border: none;
    border-radius: var(--radius-s);
    color: var(--text-3);
    font-size: 12px;
    cursor: pointer;
  }

  .seg button:hover:not(:disabled) {
    color: var(--text-1);
  }

  .seg button.on {
    background: var(--bg-raised);
    color: var(--text-1);
    font-weight: 500;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.25);
    cursor: default;
  }

  .seg button:disabled:not(.on) {
    opacity: 0.5;
    cursor: default;
  }

  .spacer {
    flex: 1;
  }

  .elapsed {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    color: var(--text-2);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .stop {
    background: color-mix(in srgb, var(--red) 16%, transparent);
    color: var(--red);
  }

  .stop:hover {
    background: color-mix(in srgb, var(--red) 24%, transparent) !important;
  }
</style>
