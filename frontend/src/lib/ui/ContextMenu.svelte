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
    min-width: 190px;
    padding: 4px;
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-radius: 6px;
    box-shadow: 0 8px 24px var(--crust);
    outline: none;
  }

  button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    height: 26px;
    padding: 0 8px;
    background: none;
    border: none;
    border-radius: 4px;
    text-align: left;
    cursor: pointer;
  }

  button.active {
    background: var(--surface0);
  }

  button:disabled {
    opacity: 0.4;
    cursor: default;
  }

  .danger {
    color: var(--red);
  }

  .ico {
    width: 14px;
    color: var(--overlay2);
  }

  .danger .ico {
    color: var(--red);
  }

  .label {
    flex: 1;
  }

  .sep {
    height: 1px;
    margin: 4px 6px;
    background: var(--surface0);
  }
</style>
