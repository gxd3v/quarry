<script lang="ts">
  import { errorMessage } from './lib/api/errors'
  import { serverInfo, type ServerInfo } from './lib/api/server'
  import Shell from './lib/components/Shell.svelte'
  import DevBench from './lib/dev/DevBench.svelte'

  let info = $state<Promise<ServerInfo>>(serverInfo())
</script>

{#await info then i}
  {#if i.bench}
    <DevBench />
  {:else}
    <Shell />
  {/if}
{:catch err}
  <div class="down">
    <p>Cannot reach the Quarry backend: {errorMessage(err)}</p>
    <button class="btn" type="button" onclick={() => (info = serverInfo())}>Retry</button>
  </div>
{/await}

<style>
  .down {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    height: 100%;
    color: var(--red);
  }
</style>
