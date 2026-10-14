<script lang="ts">
  import { menu, type MenuItem } from '../app/menu.svelte'
  import Icon from './Icon.svelte'

  let el = $state<HTMLDivElement>()
  let pos = $state({ x: 0, y: 0 })
  let active = $state(-1)

  const items = $derived(menu.current?.items ?? [])

  $effect(() => {
    const cur = menu.current
    if (!cur || !el) return
    // Keep the menu inside the window.
    const r = el.getBoundingClientRect()
    pos = {
      x: Math.max(4, Math.min(cur.x, window.innerWidth - r.width - 4)),
      y: Math.max(4, Math.min(cur.y, window.innerHeight - r.height - 4)),
    }
    active = -1
    el.focus()
  })

  function pick(item: MenuItem) {
    if (item.disabled) return
    menu.close()
    item.run()
  }

  function move(dir: number) {
    const n = items.length
    for (let i = 1; i <= n; i++) {
      const j = (((active + dir * i) % n) + n) % n
      const it = items[j]
      if (it !== 'separator' && !it.disabled) {
        active = j
        return
      }
    }
  }

  function onkeydown(e: KeyboardEvent) {
    e.stopPropagation()
    if (e.key === 'ArrowDown') move(1)
    else if (e.key === 'ArrowUp') move(-1)
    else if (e.key === 'Escape' || e.key === 'Tab') menu.close()
    else if (e.key === 'Enter' || e.key === ' ') {
      const it = items[active]
      if (it && it !== 'separator') pick(it)
    } else return
    e.preventDefault()
  }
</script>

{#if menu.current}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="backdrop" onpointerdown={() => menu.close()} oncontextmenu={(e) => (e.preventDefault(), menu.close())}></div>
  <div
    class="menu"
    role="menu"
    tabindex="-1"
    bind:this={el}
    style:left="{pos.x}px"
    style:top="{pos.y}px"
    {onkeydown}
  >
    {#each items as item, i (i)}
      {#if item === 'separator'}
        <div class="sep" role="separator"></div>
      {:else}
        <button
          type="button"
          role="menuitem"
          class:danger={item.danger}
          class:active={i === active}
          disabled={item.disabled}
          tabindex="-1"
          onpointerenter={() => (active = item.disabled ? -1 : i)}
          onclick={() => pick(item)}
        >
          <span class="ico">{#if item.icon}<Icon name={item.icon} size={13} />{/if}</span>
          <span class="label">{item.label}</span>
          {#if item.hint}<span class="kbd">{item.hint}</span>{/if}
        </button>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
  }

  .menu {
    position: fixed;
    z-index: 51;
    min-width: 200px;
    padding: 5px;
    background: color-mix(in srgb, var(--surface0) 35%, var(--bg-main));
    border: 1px solid var(--line-strong);
    border-radius: var(--radius-l);
    box-shadow: var(--shadow-pop);
    outline: none;
    animation: in 0.1s ease-out;
  }

  @keyframes in {
    from {
      opacity: 0;
      transform: scale(0.98);
    }
  }

  button {
    display: flex;
    align-items: center;
    gap: 9px;
    width: 100%;
    height: 28px;
    padding: 0 9px;
    background: none;
    border: none;
    border-radius: var(--radius);
    color: var(--text-1);
    text-align: left;
    cursor: default;
  }

  button.active {
    background: var(--bg-active);
  }

  button:disabled {
    opacity: 0.4;
  }

  .danger {
    color: var(--red);
  }

  .ico {
    display: inline-flex;
    width: 14px;
    color: var(--text-3);
  }

  .danger .ico {
    color: var(--red);
  }

  .label {
    flex: 1;
  }

  .sep {
    height: 1px;
    margin: 5px 4px;
    background: var(--line);
  }
</style>
