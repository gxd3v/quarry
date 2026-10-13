<script lang="ts">
  import { connections, ConnectionState } from '../app/connections.svelte'
  import type { ConnectionRequest } from '../app/ui.svelte'
  import Modal from '../ui/Modal.svelte'
  import PickerList, { type PickItem } from '../ui/PickerList.svelte'

  interface Props {
    request: ConnectionRequest
    onnew: () => void
  }

  let { request, onnew }: Props = $props()

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

<Modal title={request.title} width={420} onclose={() => request.resolve(null)}>
  {#if items.length}
    <PickerList {items} placeholder="Connection" onpick={(it) => request.resolve(it.key)} />
  {:else}
    <p>No connections yet.</p>
    <button
      class="btn primary"
      type="button"
      onclick={() => {
        request.resolve(null)
        onnew()
      }}>New connection</button
    >
  {/if}
</Modal>
