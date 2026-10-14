<script lang="ts">
  import { saveExportDialog } from '../api/server'
  import { exportTasks, MAX_RUNNING } from '../app/exports.svelte'
  import { notify } from '../app/notify.svelte'
  import type { ExportRequest } from '../app/ui.svelte'
  import { applies, DEFAULT_FORM, EXPORT_FORMATS, exportOptions, fileName, validate, withExtension, type ExportForm } from '../export/options'
  import Modal from '../ui/Modal.svelte'

  interface Props {
    request: ExportRequest
    onclose: () => void
  }

  let { request, onclose }: Props = $props()

  const FORM_KEY = 'quarry.export'

  // The last form's options (not its path) are remembered.
  function lastForm(): ExportForm {
    try {
      const v = JSON.parse(localStorage.getItem(FORM_KEY) ?? '{}') as Partial<ExportForm>
      return { ...DEFAULT_FORM, ...v, path: '', table: '' }
    } catch {
      return { ...DEFAULT_FORM }
    }
  }

  let form = $state<ExportForm>(lastForm())
  let error = $state('')
  const opt = $derived(applies(form.format))

  const source = $derived.by(() => {
    const s = request.source
    if ('sql' in s) return { kind: 'Statement', text: s.sql.replace(/\s+/g, ' ').trim() }
    const d = s.data
    const parts = [d.node.name]
    if (d.filter) parts.push(`WHERE ${d.filter}`)
    if (d.orderBy.length) parts.push(`ORDER BY ${d.orderBy.join(', ')}`)
    return { kind: 'Table data', text: parts.join(' ') }
  })

  function setFormat(f: ExportForm['format']) {
    form.format = f
    form.crlf = f === 'csv'
    form.path = withExtension(form.path, f)
  }

  async function browse() {
    try {
      const path = await saveExportDialog(fileName(request.name, form.format), form.format)
      if (path) form.path = path
    } catch (err) {
      notify.error(err, 'File dialog')
    }
  }

  function submit(e: SubmitEvent) {
    e.preventDefault()
    error = validate(form)
    if (error) return
    try {
      const { path: _path, table: _table, ...keep } = form
      localStorage.setItem(FORM_KEY, JSON.stringify(keep))
    } catch {
      // Remembering the options is a convenience.
    }
    const queued = exportTasks.active.length >= MAX_RUNNING
    exportTasks.start({
      connId: request.connId,
      scope: { ...request.scope },
      source: request.source,
      path: form.path.trim(),
      options: exportOptions(form),
      label: request.name,
    })
    notify.flash(queued ? `Export of ${request.name} queued` : `Exporting ${request.name}…`)
    onclose()
  }
</script>

<Modal title="Export {request.name}" width={520} {onclose}>
  <form id="export-form" class="export" onsubmit={submit}>
    <p class="source"><span class="k">{source.kind}</span> <code title={source.text}>{source.text}</code></p>

    <label for="exp-format">Format</label>
    <select id="exp-format" class="field" value={form.format} onchange={(e) => setFormat((e.currentTarget as HTMLSelectElement).value as ExportForm['format'])}>
      {#each EXPORT_FORMATS as f (f.format)}<option value={f.format}>{f.label}</option>{/each}
    </select>

    <label for="exp-path">File</label>
    <div class="path">
      <input id="exp-path" class="field mono" type="text" placeholder="Absolute path, e.g. C:\exports\{fileName(request.name, form.format)}" bind:value={form.path} />
      <button class="btn" type="button" onclick={browse}>Browse…</button>
    </div>

    {#if opt.delimiter}
      <label for="exp-delim">Delimiter</label>
      <input id="exp-delim" class="field mono short" type="text" maxlength="2" placeholder="," title="One character; \t for tab" bind:value={form.delimiter} />
    {/if}
    {#if opt.nullText}
      <label for="exp-null">NULL as</label>
      <input id="exp-null" class="field mono short" type="text" placeholder="(empty)" bind:value={form.nullText} />
    {/if}
    {#if opt.table}
      <label for="exp-table">Table</label>
      <input id="exp-table" class="field mono" type="text" placeholder="the source table, or t" bind:value={form.table} />
      <label for="exp-batch">Rows per INSERT</label>
      <input id="exp-batch" class="field short" type="number" min="1" bind:value={form.batchRows} />
    {/if}

    <span class="label">Options</span>
    <div class="checks">
      {#if opt.header}<label><input type="checkbox" bind:checked={form.header} /> Header row</label>{/if}
      {#if opt.quoteAll}<label><input type="checkbox" bind:checked={form.quoteAll} /> Quote all values</label>{/if}
      <label><input type="checkbox" bind:checked={form.crlf} /> CRLF line endings</label>
      <label><input type="checkbox" bind:checked={form.bom} /> UTF-8 BOM</label>
    </div>
    {#if error}<p class="error">{error}</p>{/if}
  </form>
  {#snippet footer()}
    <span class="note">Runs in the background on its own session; Cancel removes the partial file.</span>
    <button class="btn" type="button" onclick={onclose}>Cancel</button>
    <button class="btn primary" type="submit" form="export-form">Export</button>
  {/snippet}
</Modal>

<style>
  .export {
    display: grid;
    grid-template-columns: 110px 1fr;
    gap: 8px 12px;
    align-items: center;
  }

  .export > label,
  .label {
    color: var(--subtext0);
    font-size: 12px;
  }

  .source {
    grid-column: 1 / -1;
    display: flex;
    gap: 8px;
    margin: 0 0 4px;
    min-width: 0;
    font-size: 12px;
  }

  .source .k {
    flex: none;
    color: var(--overlay1);
  }

  .source code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--subtext1);
    font-family: var(--font-mono);
  }

  .path {
    display: flex;
    gap: 6px;
  }

  .mono {
    font-family: var(--font-mono);
  }

  .short {
    width: 120px;
  }

  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
  }

  .checks label {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .error {
    grid-column: 1 / -1;
    margin: 0;
    color: var(--red);
  }

  .note {
    margin-right: auto;
    color: var(--overlay1);
    font-size: 11px;
  }
</style>
