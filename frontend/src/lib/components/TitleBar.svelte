<script lang="ts">
  import { onMount } from 'svelte'
  import { appWindow, platform } from '../api/server'
  import { settings } from '../app/settings.svelte'
  import { ui } from '../app/ui.svelte'
  import { workspace } from '../app/workspace.svelte'
  import { keymap, label } from '../keymap'
  import Icon from '../ui/Icon.svelte'
  import TabBar from './TabBar.svelte'

  let { lead }: { lead: number } = $props()

  const controls = platform !== 'mac' && appWindow.available()
  let maximised = $state(false)

  onMount(() => {
    if (!controls) return
    const sync = async () => (maximised = await appWindow.isMaximised())
    void sync()
    window.addEventListener('resize', sync)
    return () => window.removeEventListener('resize', sync)
  })

  const paletteHint = $derived.by(() => {
    void settings.keys
    return label(keymap.keysFor('palette.commands')[0] ?? '')
  })

  // A double click on empty title bar space maximises, as native title bars do.
  function ondblclick(e: MouseEvent) {
    if (!controls || !(e.target instanceof HTMLElement) || !e.target.closest('[data-drag]')) return
    if (getComputedStyle(e.target).getPropertyValue('--wails-draggable').trim() !== 'drag') return
    appWindow.toggleMaximise()
  }

  async function close() {
    await workspace.flush()?.catch(() => {})
    appWindow.quit()
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<header class="titlebar" class:mac={platform === 'mac'} data-drag {ondblclick}>
  <div class="lead" style:width="{lead}px" data-drag>
    <span class="logo" aria-hidden="true">
      <svg viewBox="0 0 20 20" width="18" height="18">
        <path d="M10 1.8 17.5 6v8L10 18.2 2.5 14V6z" fill="currentColor" opacity=".18" />
        <path d="M10 1.8 17.5 6 10 10.2 2.5 6z" fill="currentColor" opacity=".9" />
        <path d="M10 10.2V18.2L2.5 14V6z" fill="currentColor" opacity=".45" />
      </svg>
    </span>
    <span class="name">Quarry</span>
  </div>
  <div class="tabs" data-drag><TabBar /></div>
  <button class="search" type="button" onclick={() => (ui.palette = 'commands')} title="Command palette">
    <Icon name="search" size={13} />
    <span>Commands</span>
    {#if paletteHint}<span class="kbd">{paletteHint}</span>{/if}
  </button>
  {#if controls}
    <div class="controls">
      <button type="button" aria-label="Minimise" onclick={() => appWindow.minimise()}>
        <svg viewBox="0 0 12 12" width="12" height="12"><path d="M2 6.5h8" stroke="currentColor" stroke-width="1.1" /></svg>
      </button>
      <button type="button" aria-label={maximised ? 'Restore' : 'Maximise'} onclick={() => appWindow.toggleMaximise()}>
        {#if maximised}
          <svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.1"><rect x="2" y="3.5" width="6.5" height="6.5" rx="1" /><path d="M4 3.5V2.6c0-.3.3-.6.6-.6h4.8c.3 0 .6.3.6.6v4.8c0 .3-.3.6-.6.6h-.9" /></svg>
        {:else}
          <svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.1"><rect x="2" y="2" width="8" height="8" rx="1.2" /></svg>
        {/if}
      </button>
      <button class="close" type="button" aria-label="Close" onclick={close}>
        <svg viewBox="0 0 12 12" width="12" height="12" stroke="currentColor" stroke-width="1.1"><path d="m2.5 2.5 7 7m0-7-7 7" /></svg>
      </button>
    </div>
  {/if}
</header>

<style>
  .titlebar {
    --wails-draggable: drag;
    display: flex;
    align-items: stretch;
    height: 40px;
    background: var(--bg-chrome);
    user-select: none;
  }

  .lead {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: none;
    padding-left: 14px;
    color: var(--text-2);
  }

  .mac .lead {
    padding-left: 80px;
  }

  .logo {
    display: inline-flex;
    color: var(--accent);
  }

  .name {
    font-weight: 600;
    letter-spacing: 0.01em;
  }

  .tabs {
    flex: 1;
    min-width: 0;
    padding-left: 10px;
  }

  .search {
    --wails-draggable: no-drag;
    align-self: center;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex: none;
    height: 26px;
    margin: 0 10px 0 12px;
    padding: 0 6px 0 10px;
    background: var(--bg-hover);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    color: var(--text-3);
    font-size: 12px;
    cursor: pointer;
    transition:
      background 0.12s,
      color 0.12s;
  }

  .search:hover {
    background: var(--bg-active);
    color: var(--text-2);
  }

  .search .kbd {
    background: transparent;
    padding: 0;
  }

  .controls {
    --wails-draggable: no-drag;
    display: flex;
    flex: none;
  }

  .controls button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 46px;
    height: 100%;
    padding: 0;
    background: transparent;
    border: none;
    color: var(--text-2);
    cursor: default;
    transition: background 0.1s;
  }

  .controls button:hover {
    background: var(--bg-hover);
    color: var(--text-1);
  }

  .controls .close:hover {
    background: #e81123;
    color: #fff;
  }
</style>
