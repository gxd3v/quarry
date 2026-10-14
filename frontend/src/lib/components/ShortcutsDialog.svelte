<script lang="ts">
  import { notify } from '../app/notify.svelte'
  import { settings } from '../app/settings.svelte'
  import { chordOf, keymap, label, type Action } from '../keymap'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    onclose: () => void
  }

  let { onclose }: Props = $props()

  let filter = $state('')
  // The action whose new chord is being recorded, and a chord that conflicts.
  let recording = $state<string | null>(null)
  let conflict = $state<{ chord: string; other: Action | undefined; otherId: string } | null>(null)

  const name = (a: Action) => (a.category ? `${a.category}: ${a.title}` : a.title)

  const rows = $derived.by(() => {
    void settings.keys
    const q = filter.trim().toLowerCase()
    return keymap
      .actions()
      .map((a) => ({ a, keys: keymap.keysFor(a.id), custom: a.id in keymap.overrides }))
      .filter((r) => !q || name(r.a).toLowerCase().includes(q) || r.keys.some((k) => label(k).toLowerCase().includes(q)))
      .sort((x, y) => name(x.a).localeCompare(name(y.a)))
  })

  const customized = $derived.by(() => {
    void settings.keys
    return Object.keys(keymap.overrides).length > 0
  })

  async function apply(overrides: Record<string, string>) {
    try {
      await settings.setKeymap(overrides)
    } catch (err) {
      notify.error(err, 'Save shortcuts')
    }
  }

  function assign(id: string, chord: string) {
    recording = null
    conflict = null
    void apply({ ...keymap.overrides, [id]: chord })
  }

  function reset(id: string) {
    const next = { ...keymap.overrides }
    delete next[id]
    void apply(next)
  }

  function onkeydown(e: KeyboardEvent) {
    if (!recording) return
    e.preventDefault()
    e.stopPropagation()
    if (e.key === 'Escape') {
      recording = null
      conflict = null
      return
    }
    const chord = chordOf(e)
    if (!chord) return
    const otherId = keymap.boundTo(chord)
    if (otherId && otherId !== recording) {
      conflict = { chord, other: keymap.action(otherId), otherId }
      return
    }
    assign(recording, chord)
  }

  $effect(() => {
    window.addEventListener('keydown', onkeydown, true)
    return () => window.removeEventListener('keydown', onkeydown, true)
  })
</script>

<Modal title="Keyboard shortcuts" width={640} {onclose}>
  <input class="field" type="text" placeholder="Filter by command or key" bind:value={filter} />
  <div class="list">
    {#each rows as r (r.a.id)}
      <div class="row" class:recording={recording === r.a.id}>
        <span class="title" title={r.a.id}>{name(r.a)}</span>
        {#if recording === r.a.id}
          {#if conflict}
            <span class="warn">
              <span class="kbd">{label(conflict.chord)}</span> runs “{conflict.other ? name(conflict.other) : conflict.otherId}”.
            </span>
            <button class="btn small" type="button" onclick={() => assign(r.a.id, conflict!.chord)}>Reassign</button>
            <button class="btn small" type="button" onclick={() => (conflict = null)}>Choose another</button>
          {:else}
            <span class="hint">Waiting for a key combination · Esc cancels</span>
          {/if}
        {:else}
          <button class="keys" type="button" title="Record a new shortcut" onclick={() => ((recording = r.a.id), (conflict = null))}>
            {#each r.keys as k (k)}<span class="kbd">{label(k)}</span>{:else}<span class="none">unbound</span>{/each}
          </button>
          <span class="actions">
            {#if r.custom}
              <button class="link" type="button" onclick={() => reset(r.a.id)}>Reset</button>
            {/if}
            {#if r.keys.length}
              <button class="link" type="button" onclick={() => assign(r.a.id, '')}>Remove</button>
            {/if}
          </span>
        {/if}
      </div>
    {/each}
  </div>
  {#snippet footer()}
    <span class="foot">Saved in settings.json under "keymap".</span>
    <button class="btn" type="button" disabled={!customized} onclick={() => apply({})}
      >Reset all</button
    >
    <button class="btn primary" type="button" onclick={onclose}>Done</button>
  {/snippet}
</Modal>

<style>
  .list {
    margin-top: 8px;
    max-height: min(460px, 60vh);
    overflow: auto;
    border: 1px solid var(--surface0);
    border-radius: 5px;
    background: var(--crust);
  }

  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
    padding: 0 10px;
    border-bottom: 1px solid var(--surface0);
  }

  .row:last-child {
    border-bottom: none;
  }

  .row.recording {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
  }

  .title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .keys {
    display: inline-flex;
    gap: 4px;
    min-width: 120px;
    padding: 2px 4px;
    background: none;
    border: 1px dashed transparent;
    border-radius: 4px;
    cursor: pointer;
  }

  .keys:hover {
    border-color: var(--surface2);
  }

  .kbd {
    padding: 1px 5px;
    border: 1px solid var(--surface1);
    border-radius: 4px;
    color: var(--subtext1);
  }

  .none,
  .hint {
    color: var(--overlay0);
    font-size: 12px;
  }

  .warn {
    color: var(--peach);
    font-size: 12px;
  }

  .actions {
    display: inline-flex;
    gap: 8px;
    width: 96px;
    justify-content: flex-end;
  }

  .link {
    padding: 0;
    background: none;
    border: none;
    color: var(--blue);
    cursor: pointer;
    font-size: 12px;
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }

  .foot {
    margin-right: auto;
    color: var(--overlay0);
    font-size: 12px;
  }
</style>
