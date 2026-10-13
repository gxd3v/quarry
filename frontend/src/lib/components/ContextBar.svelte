<script lang="ts">
  import { guard, run } from '../app/actions'
  import { connections } from '../app/connections.svelte'
  import { tabs, type Tab } from '../app/tabs.svelte'
  import { ui } from '../app/ui.svelte'
  import { scopeLabel } from '../sql'
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

<div class="ctx" data-env={env}>
  <span class="badge" class:prod={env === 'prod'} title="Environment">{env.toUpperCase()}</span>

  <button class="target" type="button" title="Change connection or scope" onclick={() => (ui.scoping = true)}>
    <span class="conn">{cfg?.name ?? 'unknown connection'}</span>
    {#if scope}<span class="sep">›</span><span class="scope">{scope}</span>{/if}
    {#if cfg?.readOnly}<span class="ro" title="Read-only connection">RO</span>{/if}
    <Icon name="chevron" size={10} />
  </button>

  {#if tab.tx.supported}
    <button
      class="commit-mode"
      class:manual={!tab.tx.autoCommit}
      type="button"
      title={tab.tx.autoCommit ? 'Auto-commit: each statement commits. Click for manual transactions.' : 'Manual: commit or roll back yourself. Click for auto-commit.'}
      disabled={tab.busy !== ''}
      onclick={() => guard('Change commit mode', () => tabs.setAutoCommit(tab, !tab.tx.autoCommit))}
    >
      {tab.tx.autoCommit ? 'AUTO' : 'MANUAL'}
    </button>
  {/if}

  {#if tab.tx.inTx}
    <span class="tx" title="A transaction is open">● TX</span>
    <button class="btn small commit" type="button" disabled={tab.busy !== ''} onclick={() => guard('Commit', () => tabs.commit(tab))}
      >Commit</button
    >
    <button class="btn small danger" type="button" disabled={tab.busy !== ''} onclick={() => guard('Rollback', () => tabs.rollback(tab))}
      >Rollback</button
    >
  {/if}

  <span class="spacer"></span>

  {#if tab.busy}
    <span class="elapsed" title={tab.busy === 'fetch' ? 'Fetching' : 'Running'}>
      <span class="spinner"></span>{tab.canceling ? 'Canceling…' : fmt(elapsed)}
    </span>
  {/if}
  <button class="btn small run" type="button" title="Run statement or selection (Ctrl+Enter) · whole script (Ctrl+Shift+Enter)" onclick={() => run(tab)}>
    <Icon name="play" size={12} fill /> Run
  </button>
  <button class="btn small stop" type="button" title="Stop (Esc)" disabled={!tab.busy} onclick={() => tabs.cancel(tab)}>
    <Icon name="stop" size={12} fill /> Stop
  </button>
</div>

<style>
  .ctx {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 10px;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    box-shadow: inset 3px 0 0 var(--env);
  }

  .badge {
    padding: 2px 7px;
    border: 1px solid var(--env);
    border-radius: 4px;
    color: var(--env);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
  }

  .badge.prod {
    background: var(--env-prod);
    color: var(--crust);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--env-prod) 35%, transparent);
  }

  .target {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    height: 26px;
    padding: 0 8px;
    background: none;
    border: 1px solid transparent;
    border-radius: 5px;
    cursor: pointer;
  }

  .target:hover {
    background: var(--surface0);
    border-color: var(--surface1);
  }

  .target :global(.icon) {
    color: var(--overlay1);
    transform: rotate(90deg);
  }

  .conn {
    font-weight: 600;
    white-space: nowrap;
  }

  .sep {
    color: var(--overlay0);
  }

  .scope {
    color: var(--subtext0);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .ro {
    padding: 0 4px;
    border-radius: 3px;
    background: var(--surface1);
    color: var(--yellow);
    font-size: 10px;
    font-weight: 700;
  }

  .commit-mode {
    height: 22px;
    padding: 0 8px;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 11px;
    color: var(--subtext0);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.05em;
    cursor: pointer;
  }

  .commit-mode.manual {
    color: var(--peach);
    border-color: var(--peach);
  }

  .commit-mode:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .tx {
    color: var(--peach);
    font-weight: 700;
    font-size: 12px;
  }

  .btn.small {
    height: 24px;
    padding: 0 9px;
  }

  .commit {
    color: var(--green);
  }

  .spacer {
    flex: 1;
  }

  .elapsed {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--subtext0);
    font-family: var(--font-mono);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .run {
    color: var(--green);
  }

  .stop {
    color: var(--red);
  }
</style>
