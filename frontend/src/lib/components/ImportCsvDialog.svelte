<script lang="ts">
  import { importClient } from '../api/clients'
  import { errorMessage, isAbort, needsConfirmation } from '../api/errors'
  import { openFileDialog } from '../api/server'
  import { connections } from '../app/connections.svelte'
  import { notify } from '../app/notify.svelte'
  import { prompts } from '../app/prompts.svelte'
  import { loadData } from '../app/table.svelte'
  import { tabs } from '../app/tabs.svelte'
  import type { ImportRequest } from '../app/ui.svelte'
  import type { PreviewCSVResponse } from '../gen/quarry/v1/import_pb'
  import { DELIMITERS, ENCODINGS, keepMapping, mappingProblem, nullText } from '../import/csv'
  import Modal from '../ui/Modal.svelte'

  let { request, onclose }: { request: ImportRequest; onclose: () => void } = $props()

  let path = $state('')
  let preview = $state.raw<PreviewCSVResponse | null>(null)
  let delimiter = $state(',')
  let header = $state(true)
  let encoding = $state('UTF-8')
  let mapping = $state<string[]>([])
  let nullMode = $state<'empty' | 'text' | 'none'>('empty')
  let nullValue = $state('NULL')
  let busy = $state<'' | 'preview' | 'import'>('')
  let error = $state('')
  let rows = $state(0)
  let ctrl: AbortController | null = null

  const target = $derived(request.node.path.slice(-1)[0] ?? request.node.name)
  const problem = $derived(preview ? mappingProblem(mapping) : 'Choose a file')

  async function load(withFormat: boolean) {
    if (!path.trim()) return
    busy = 'preview'
    error = ''
    try {
      const res = await connections.withConnection(request.connId, () =>
        importClient().previewCSV({
          connId: request.connId,
          table: request.node,
          path: path.trim(),
          format: withFormat ? { delimiter, hasHeader: header, encoding } : undefined,
        }),
      )
      mapping = keepMapping(preview?.header ?? [], mapping, res.header, res.mapping)
      preview = res
      delimiter = res.format?.delimiter || ','
      header = res.format?.hasHeader ?? true
      encoding = res.format?.encoding || 'UTF-8'
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function browse() {
    try {
      const p = await openFileDialog('Import CSV', 'CSV and text files', '*.csv;*.tsv;*.txt')
      if (!p) return
      path = p
      preview = null
      await load(false)
    } catch (err) {
      error = errorMessage(err)
    }
  }

  async function run(e: SubmitEvent) {
    e.preventDefault()
    if (problem) return
    busy = 'import'
    error = ''
    rows = 0
    ctrl = new AbortController()
    const env = connections.env(request.connId)
    let confirmed = false
    const confirm = () =>
      prompts.ask({
        title: env === 'prod' ? `Import on PROD: ${connections.name(request.connId)}` : 'Confirm import',
        text: `Inserts every row of ${path.split(/[\\/]/).pop()} into ${target} in one transaction.`,
        confirm: 'Import',
        danger: true,
        env,
      })
    try {
      if (env === 'prod') {
        if (!(await confirm())) return
        confirmed = true
      }
      for (;;) {
        try {
          let done = false
          await connections.withConnection(request.connId, async () => {
            const req = {
              connId: request.connId,
              table: request.node,
              path: path.trim(),
              format: { delimiter, hasHeader: header, encoding },
              mapping,
              nullText: nullText(nullMode, nullValue),
              confirmed,
            }
            for await (const res of importClient().importCSV(req, { signal: ctrl!.signal })) {
              rows = Number(res.rows)
              if (res.done) done = true
            }
          })
          if (!done) throw new Error('the import stream ended early')
          break
        } catch (err) {
          if (confirmed || !needsConfirmation(err)) throw err
          if (!(await confirm())) return
          confirmed = true
        }
      }
      notify.flash(`Imported ${rows.toLocaleString('en-US')} rows into ${target}`)
      const open = tabs.tabs.find((t) => t.connId === request.connId && t.table && t.table.node.path.join('\u0000') === request.node.path.join('\u0000'))
      if (open) void loadData(open, { force: true })
      onclose()
    } catch (err) {
      error = ctrl?.signal.aborted || isAbort(err) ? 'Import canceled; nothing was saved' : errorMessage(err)
    } finally {
      busy = ''
      ctrl = null
    }
  }

  function cancel() {
    if (ctrl) ctrl.abort()
    else onclose()
  }
</script>

<Modal title="Import CSV into {target}" width={760} env={connections.env(request.connId)} onclose={cancel}>
  <form id="import-csv" onsubmit={run} autocomplete="off">
    <div class="file">
      <input
        class="field mono"
        type="text"
        placeholder="Path of the CSV file"
        bind:value={path}
        onkeydown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            void load(false)
          }
        }}
      />
      <button class="btn" type="button" disabled={busy !== ''} onclick={browse}>Browse…</button>
      <button class="btn" type="button" disabled={busy !== '' || !path.trim()} onclick={() => load(false)}>Read</button>
    </div>

    {#if preview}
      <div class="format">
        <label>
          <span>Delimiter</span>
          <select class="field" bind:value={delimiter} onchange={() => load(true)}>
            {#each DELIMITERS as d (d.value)}<option value={d.value}>{d.label}</option>{/each}
          </select>
        </label>
        <label>
          <span>Encoding</span>
          <select class="field" bind:value={encoding} onchange={() => load(true)}>
            {#each ENCODINGS as e (e)}<option value={e}>{e}</option>{/each}
          </select>
        </label>
        <label class="check"><input type="checkbox" bind:checked={header} onchange={() => load(true)} /> First row is a header</label>
        <label>
          <span>NULL</span>
          <select class="field" bind:value={nullMode}>
            <option value="empty">Empty cells</option>
            <option value="text">Cells equal to…</option>
            <option value="none">Never</option>
          </select>
        </label>
        {#if nullMode === 'text'}
          <label><span>NULL text</span><input class="field mono" type="text" bind:value={nullValue} /></label>
        {/if}
      </div>

      <div class="preview">
        <table>
          <thead>
            <tr>
              {#each preview.header as h, i (i)}
                <th>
                  <div class="src">{h}</div>
                  <select class="field map" class:skip={!mapping[i]} bind:value={mapping[i]}>
                    <option value="">— skip —</option>
                    {#each preview.tableColumns as c (c)}<option value={c}>{c}</option>{/each}
                  </select>
                </th>
              {/each}
            </tr>
          </thead>
          <tbody>
            {#each preview.rows as r, i (i)}
              <tr>
                {#each preview.header as _, j (j)}<td class:skip={!mapping[j]}>{r.cells[j] ?? ''}</td>{/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if busy === 'preview'}
      <p class="muted"><span class="spinner"></span> Reading…</p>
    {/if}

    {#if busy === 'import'}
      <p class="progress"><span class="spinner"></span> Imported {rows.toLocaleString('en-US')} rows…</p>
    {/if}
    {#if error}<p class="error">{error}</p>{:else if preview && problem}<p class="hint">{problem}</p>{/if}
  </form>

  {#snippet footer()}
    <span class="note">All rows go in one transaction; any bad row rolls the import back.</span>
    <span class="spacer"></span>
    <button class="btn" type="button" onclick={cancel}>{busy === 'import' ? 'Stop' : 'Cancel'}</button>
    <button class="btn primary" type="submit" form="import-csv" disabled={busy !== '' || !!problem}>Import</button>
  {/snippet}
</Modal>

<style>
  .file {
    display: flex;
    gap: 6px;
  }

  .file input {
    flex: 1;
  }

  .format {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 12px;
    margin: 12px 0;
  }

  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  label > span {
    color: var(--subtext0);
    font-size: 12px;
  }

  .check {
    flex-direction: row;
    align-items: center;
    gap: 6px;
    height: 28px;
  }

  .check input {
    accent-color: var(--accent);
  }

  .preview {
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--surface0);
    border-radius: 6px;
  }

  table {
    border-collapse: collapse;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  th,
  td {
    padding: 3px 8px;
    border-right: 1px solid var(--surface0);
    border-bottom: 1px solid var(--surface0);
    white-space: nowrap;
    max-width: 240px;
    overflow: hidden;
    text-overflow: ellipsis;
    text-align: left;
  }

  th {
    position: sticky;
    top: 0;
    background: var(--mantle);
    vertical-align: top;
  }

  .src {
    color: var(--subtext0);
    font-weight: normal;
    margin-bottom: 3px;
  }

  .map {
    height: 24px;
    font-size: 12px;
  }

  .map.skip,
  td.skip {
    color: var(--faint);
  }

  .muted,
  .hint,
  .note {
    color: var(--subtext0);
  }

  .note {
    font-size: 12px;
  }

  .progress {
    margin: 10px 0 0;
  }

  .error {
    margin: 10px 0 0;
    padding: 6px 10px;
    border-radius: 5px;
    color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
    white-space: pre-wrap;
  }

  .spacer {
    flex: 1;
  }
</style>
