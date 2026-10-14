<script lang="ts">
  import { insertText } from '../app/actions'
  import { openTable, tableNode } from '../app/table.svelte'
  import { connections } from '../app/connections.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import { explorerClient } from '../api/clients'
  import { errorMessage } from '../api/errors'
  import type { CatalogObject } from '../gen/quarry/v1/explorer_pb'
  import { quoteIdent, scopeLabel } from '../sql'
  import FuzzyList from '../ui/FuzzyList.svelte'
  import Icon from '../ui/Icon.svelte'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    tab: Tab
    onclose: () => void
  }

  let { tab, onclose }: Props = $props()

  let objects = $state.raw<CatalogObject[]>([])
  let loading = $state(true)
  let error = $state('')

  const OPENABLE = new Set(['table', 'view'])
  const ICONS: Record<string, string> = { table: 'table', view: 'view', function: 'function', sequence: 'sequence' }

  async function load(refresh: boolean) {
    const connId = tab.connId
    const scope = { database: tab.scope.database, schema: tab.scope.schema }
    loading = true
    error = ''
    try {
      const res = await connections.withConnection(connId, () => explorerClient().catalog({ connId, scope, refresh }))
      objects = res.objects
    } catch (err) {
      error = errorMessage(err)
    } finally {
      loading = false
    }
  }

  $effect(() => {
    void load(false)
  })

  const qualified = (o: CatalogObject) => (o.schema ? `${o.schema}.${o.name}` : o.name)
  const quoted = (o: CatalogObject) => (o.schema ? `${quoteIdent(o.schema)}.${quoteIdent(o.name)}` : quoteIdent(o.name))

  function pick(o: CatalogObject, e: KeyboardEvent | MouseEvent) {
    onclose()
    if (!e.altKey && OPENABLE.has(o.kind)) {
      const db = tab.scope.database || connections.config(tab.connId)?.database || ''
      openTable(tab.connId, tableNode(db, o.schema, o.name, o.kind, o.detail))
      return
    }
    setTimeout(() => insertText(quoted(o)), 0)
  }

  const where = $derived(`${connections.name(tab.connId)}${scopeLabel(tab.scope) ? ' › ' + scopeLabel(tab.scope) : ''}`)
</script>

<Modal title="Go to object in {where}" width={620} {onclose}>
  {#if error}
    <p class="error">{error} <button class="btn small" type="button" onclick={() => load(true)}>Retry</button></p>
  {/if}
  <FuzzyList
    items={objects}
    key={qualified}
    onpick={pick}
    {loading}
    placeholder="Table, view, function or sequence"
    empty={objects.length ? 'Nothing matches' : 'No objects'}
  >
    {#snippet row(o, segs)}
      <span class="kind {o.kind}"><Icon name={ICONS[o.kind] ?? 'table'} size={13} /></span>
      <span class="label">{#each segs as s, i (i)}{#if s.hit}<mark>{s.text}</mark>{:else}{s.text}{/if}{/each}</span>
      {#if o.detail}<span class="detail">{o.detail}</span>{/if}
      <span class="k">{o.kind}</span>
    {/snippet}
  </FuzzyList>
  <p class="hint">
    Enter opens a table or view · Alt+Enter inserts the name ·
    <button class="link" type="button" onclick={() => load(true)} disabled={loading}>Refresh</button>
  </p>
</Modal>

<style>
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .detail {
    color: var(--overlay0);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .k {
    margin-left: auto;
    color: var(--overlay0);
    font-size: 11px;
  }

  .kind {
    color: var(--overlay2);
  }

  .kind.table {
    color: var(--blue);
  }

  .kind.view {
    color: var(--teal);
  }

  .kind.function {
    color: var(--yellow);
  }

  .kind.sequence {
    color: var(--sapphire);
  }

  .hint {
    margin: 8px 0 0;
    color: var(--overlay0);
    font-size: 12px;
  }

  .error {
    margin: 0 0 8px;
    color: var(--red);
  }

  .link {
    padding: 0;
    background: none;
    border: none;
    color: var(--blue);
    cursor: pointer;
    font: inherit;
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }
</style>
