<script lang="ts">
  import { newScript } from '../app/actions'
  import { connections, ConnectionState } from '../app/connections.svelte'
  import Modal from '../ui/Modal.svelte'
  import PickerList, { type PickItem } from '../ui/PickerList.svelte'

  interface Props {
    onclose: () => void
    onnew: () => void
  }

  let { onclose, onnew }: Props = $props()

  // Connected first, then favorites, then by name.
  const items: PickItem[] = $derived(
    connections.list
      .map((c) => c.config!)
      .map((c) => ({ c, live: connections.state(c.id) === ConnectionState.CONNECTED }))
      .sort((a, b) => Number(b.live) - Number(a.live) || Number(b.c.favorite) - Number(a.c.favorite) || a.c.name.localeCompare(b.c.name))
      .map(({ c, live }) => ({
        key: c.id,
        label: c.name,
        env: c.env || 'local',
        detail: live ? 'connected' : [c.group, c.env].filter(Boolean).join(' · '),
      })),
  )
</script>

<Modal title="New script on…" width={420} {onclose}>
  {#if items.length}
    <PickerList
      {items}
      placeholder="Connection"
      onpick={(it) => {
        onclose()
        newScript(it.key)
      }}
    />
  {:else}
    <p>No connections yet.</p>
    <button class="btn primary" type="button" onclick={onnew}>New connection</button>
  {/if}
</Modal>
