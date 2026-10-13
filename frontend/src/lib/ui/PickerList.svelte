<script lang="ts" module>
  export interface PickItem {
    key: string
    label: string
    detail?: string
    icon?: string
    env?: string
    depth?: number
    // null: not expandable; otherwise whether it is expanded.
    expanded?: boolean | null
    loading?: boolean
    error?: string
    current?: boolean
    // Caller data carried along.
    data?: unknown
  }
</script>

<script lang="ts">
  import Icon from './Icon.svelte'

  interface Props {
    items: PickItem[]
    placeholder?: string
    filter?: string
    onpick: (item: PickItem) => void
    ontoggle?: (item: PickItem) => void
  }

  let { items, placeholder = 'Filter…', filter = $bindable(''), onpick, ontoggle }: Props = $props()

  let active = $state(0)
  let list = $state<HTMLDivElement>()

  const shown = $derived.by(() => {
    const q = filter.trim().toLowerCase()
    return q ? items.filter((i) => i.label.toLowerCase().includes(q)) : items
  })

  $effect(() => {
    void shown
    active = Math.max(0, Math.min(active, shown.length - 1))
  })

  $effect(() => {
    list?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  })

  function onkeydown(e: KeyboardEvent) {
    const it = shown[active]
    switch (e.key) {
      case 'ArrowDown':
        active = Math.min(shown.length - 1, active + 1)
        break
      case 'ArrowUp':
        active = Math.max(0, active - 1)
        break
      case 'Enter':
        if (it) onpick(it)
        break
      case 'ArrowRight':
        if (!it || it.expanded !== false || !ontoggle) return
        ontoggle(it)
        break
      case 'ArrowLeft':
        if (!it || it.expanded !== true || !ontoggle) return
        ontoggle(it)
        break
      default:
        return
    }
    e.preventDefault()
  }
</script>

<div class="picker">
  <!-- svelte-ignore a11y_autofocus -->
  <input class="field" type="text" {placeholder} bind:value={filter} {onkeydown} autofocus />
  <div class="list" role="listbox" bind:this={list}>
    {#each shown as item, i (item.key)}
      <div
        class="item"
        class:active={i === active}
        class:current={item.current}
        role="option"
        aria-selected={i === active}
        tabindex="-1"
        data-i={i}
        style:padding-left="{8 + (item.depth ?? 0) * 18}px"
        onpointermove={() => (active = i)}
        onclick={() => onpick(item)}
        onkeydown={() => {}}
      >
        {#if item.expanded !== undefined && item.expanded !== null}
          <button
            class="twisty"
            class:open={item.expanded}
            type="button"
            tabindex="-1"
            aria-label={item.expanded ? 'Collapse' : 'Expand'}
            onclick={(e) => {
              e.stopPropagation()
              ontoggle?.(item)
            }}><Icon name="chevron" size={11} /></button
          >
        {:else if item.depth}
          <span class="twisty"></span>
        {/if}
        {#if item.env}<span class="env-dot" data-env={item.env}></span>{/if}
        {#if item.icon}<span class="ico"><Icon name={item.icon} size={13} /></span>{/if}
        <span class="label">{item.label}</span>
        {#if item.loading}<span class="spinner"></span>{/if}
        {#if item.error}<span class="err" title={item.error}>{item.error}</span>
        {:else if item.detail}<span class="detail">{item.detail}</span>{/if}
      </div>
    {:else}
      <div class="empty">Nothing matches</div>
    {/each}
  </div>
</div>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .list {
    max-height: 340px;
    min-height: 120px;
    overflow: auto;
    border: 1px solid var(--surface0);
    border-radius: 5px;
    background: var(--crust);
    padding: 3px;
  }

  .item {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 26px;
    padding-right: 8px;
    border-radius: 4px;
    cursor: pointer;
    white-space: nowrap;
  }

  .item.active {
    background: var(--surface0);
  }

  .item.current .label {
    color: var(--accent);
    font-weight: 600;
  }

  .twisty {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 14px;
    height: 14px;
    padding: 0;
    background: none;
    border: none;
    color: var(--overlay1);
    cursor: pointer;
    transition: transform 0.1s;
  }

  .twisty.open {
    transform: rotate(90deg);
  }

  .ico {
    color: var(--overlay2);
  }

  .label {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .detail {
    margin-left: auto;
    color: var(--overlay0);
    font-size: 12px;
  }

  .err {
    margin-left: auto;
    color: var(--red);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 50%;
  }

  .empty {
    padding: 10px;
    color: var(--overlay0);
  }
</style>
