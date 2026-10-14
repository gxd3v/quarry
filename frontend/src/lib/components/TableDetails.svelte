<script lang="ts">
  import { notify } from '../app/notify.svelte'
  import { loadDescribe, type TableView } from '../app/table.svelte'
  import type { Tab } from '../app/tabs.svelte'
  import Editor from '../editor/Editor.svelte'
  import type { DetailRow } from '../gen/quarry/v1/explorer_pb'
  import { copyText } from '../ui/clipboard'

  interface Props {
    tab: Tab
    view: Exclude<TableView, 'data'>
  }

  let { tab, view }: Props = $props()

  const t = $derived(tab.table!)
  const d = $derived(t.describe)

  $effect(() => {
    if (!t.describe && !t.describing && !t.describeError) void loadDescribe(tab)
  })

  const details = $derived.by((): DetailRow[] => {
    if (!d) return []
    switch (view) {
      case 'indexes':
        return d.indexes
      case 'constraints':
        return d.constraints
      case 'triggers':
        return d.triggers
      case 'stats':
        return d.stats
    }
    return []
  })

  async function copyDdl() {
    try {
      await copyText(d?.ddl ?? '')
      notify.flash('DDL copied')
    } catch (err) {
      notify.error(err, 'Copy')
    }
  }
</script>

<div class="details">
  {#if t.describing && !d}
    <p class="muted"><span class="spinner"></span> Loading…</p>
  {:else if t.describeError}
    <p class="error">{t.describeError} <button class="btn small" type="button" onclick={() => loadDescribe(tab, true)}>Retry</button></p>
  {:else if d}
    {#if view === 'ddl'}
      <div class="ddl-bar">
        <button class="btn small" type="button" onclick={copyDdl}>Copy</button>
        <button class="btn small" type="button" onclick={() => loadDescribe(tab, true)}>Refresh</button>
      </div>
      <div class="ddl">
        {#key d}<Editor value={d.ddl || '-- no DDL available'} readonly />{/key}
      </div>
    {:else if view === 'columns'}
      <div class="scroll">
        <table>
          <thead>
            <tr><th>#</th><th>Name</th><th>Type</th><th>Nullable</th><th>Default</th><th>Key</th><th>Comment</th></tr>
          </thead>
          <tbody>
            {#each d.columns as c, i (c.name)}
              <tr>
                <td class="n">{i + 1}</td>
                <td class="name">{c.name}</td>
                <td class="type">{c.type}</td>
                <td>{c.nullable ? 'yes' : 'no'}</td>
                <td class="mono">{c.default}</td>
                <td>{#if c.primaryKey}<span class="pk">PK</span>{/if}</td>
                <td class="comment">{c.comment}</td>
              </tr>
            {:else}
              <tr><td colspan="7" class="muted">No columns</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="scroll">
        <table>
          <thead><tr><th>Name</th><th>{view === 'stats' ? 'Value' : 'Definition'}</th></tr></thead>
          <tbody>
            {#each details as r, i (i)}
              <tr><td class="name">{r.name}</td><td class="mono def">{r.definition}</td></tr>
            {:else}
              <tr><td colspan="2" class="muted">No {view}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>

<style>
  .details {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }

  .scroll {
    flex: 1;
    overflow: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }

  th {
    position: sticky;
    top: 0;
    padding: 6px 10px;
    background: var(--bg-side);
    border-bottom: 1px solid var(--line-strong);
    color: var(--text-2);
    font-weight: 600;
    text-align: left;
    white-space: nowrap;
  }

  td {
    padding: 4px 10px;
    border-bottom: 1px solid var(--line);
    vertical-align: top;
  }

  tr:hover td {
    background: color-mix(in srgb, var(--surface0) 50%, transparent);
  }

  .n {
    color: var(--text-3);
    text-align: right;
    width: 1%;
  }

  .name {
    color: var(--text);
    font-weight: 600;
    white-space: nowrap;
  }

  .type {
    color: var(--yellow);
    font-family: var(--font-mono);
  }

  .mono {
    font-family: var(--font-mono);
  }

  .def {
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--subtext1);
  }

  .pk {
    padding: 0 4px;
    border-radius: 3px;
    background: var(--surface1);
    color: var(--yellow);
    font-size: 10px;
    font-weight: 700;
  }

  .comment {
    color: var(--text-3);
  }

  .ddl-bar {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
    padding: 4px 8px;
    background: var(--bg-side);
    border-bottom: 1px solid var(--line);
  }

  .ddl {
    flex: 1;
    min-height: 0;
  }

  .btn.small {
    height: 22px;
    padding: 0 8px;
    font-size: 12px;
  }

  .muted {
    padding: 10px;
    color: var(--text-3);
  }

  .error {
    padding: 10px;
    color: var(--red);
  }
</style>
