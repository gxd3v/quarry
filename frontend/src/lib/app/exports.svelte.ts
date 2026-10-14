// Background exports (ExportService): each holds a stream (one of the
// webview's ~6 HTTP/1.1 sockets), so at most MAX_RUNNING run at once and the
// rest wait in a queue.
import type { MessageInitShape } from '@bufbuild/protobuf'
import { exportClient } from '../api/clients'
import { errorMessage, isAbort } from '../api/errors'
import { canReveal, revealPath } from '../api/server'
import type { ExportOptionsSchema, ExportRequestSchema } from '../gen/quarry/v1/export_pb'
import type { ScopeLike } from '../sql'
import { copyText } from '../ui/clipboard'
import type { TableNodeRef } from './session'
import { connections } from './connections.svelte'
import { notify } from './notify.svelte'

export const MAX_RUNNING = 2

export type ExportSource = { sql: string } | { data: { node: TableNodeRef; filter: string; orderBy: string[] } }

export interface ExportJob {
  connId: string
  scope: ScopeLike
  source: ExportSource
  path: string
  options: MessageInitShape<typeof ExportOptionsSchema>
  // What is exported, e.g. "public.orders".
  label: string
}

export type ExportState = 'queued' | 'running' | 'done' | 'failed' | 'canceled'

export class ExportTask {
  readonly id: number
  readonly job: ExportJob
  state = $state<ExportState>('queued')
  rows = $state(0)
  // Rows per second over the last samples.
  rate = $state(0)
  startedAt = 0
  elapsedMs = $state(0)
  error = $state('')
  controller: AbortController | null = null
  #samples: { t: number; rows: number }[] = []

  constructor(id: number, job: ExportJob) {
    this.id = id
    this.job = job
  }

  progress(rows: number) {
    const now = performance.now()
    this.rows = rows
    this.elapsedMs = now - this.startedAt
    this.#samples.push({ t: now, rows })
    while (this.#samples.length > 2 && now - this.#samples[0].t > 2000) this.#samples.shift()
    const first = this.#samples[0]
    const dt = (now - first.t) / 1000
    this.rate = dt > 0.05 ? (rows - first.rows) / dt : this.elapsedMs > 0 ? rows / (this.elapsedMs / 1000) : 0
  }
}

function request(job: ExportJob): MessageInitShape<typeof ExportRequestSchema> {
  const base = { connId: job.connId, scope: { ...job.scope }, path: job.path, options: job.options }
  if ('sql' in job.source) return { ...base, source: { case: 'sql', value: job.source.sql } }
  const d = job.source.data
  return {
    ...base,
    source: { case: 'data', value: { node: { path: d.node.path, kind: d.node.kind, name: d.node.name }, filter: d.filter, orderBy: d.orderBy } },
  }
}

class Exports {
  tasks = $state<ExportTask[]>([])
  #seq = 0

  get active(): ExportTask[] {
    return this.tasks.filter((t) => t.state === 'queued' || t.state === 'running')
  }

  start(job: ExportJob): ExportTask {
    const task = new ExportTask(++this.#seq, job)
    this.tasks.push(task)
    this.#pump()
    return task
  }

  cancel(task: ExportTask) {
    if (task.state === 'queued') {
      task.state = 'canceled'
      this.#forget(task)
    } else if (task.state === 'running') {
      task.state = 'canceled'
      task.controller?.abort()
    }
  }

  #pump() {
    let running = this.tasks.filter((t) => t.state === 'running').length
    for (const t of this.tasks) {
      if (running >= MAX_RUNNING) break
      if (t.state !== 'queued') continue
      running++
      void this.#run(t)
    }
  }

  async #run(task: ExportTask) {
    const ctrl = new AbortController()
    task.controller = ctrl
    task.state = 'running'
    task.startedAt = performance.now()
    const { job } = task
    try {
      let done = false
      await connections.withConnection(job.connId, async () => {
        for await (const res of exportClient().export(request(job), { signal: ctrl.signal })) {
          task.progress(Number(res.rows))
          if (res.done) done = true
        }
      })
      if (!done) throw new Error('the export stream ended early')
      task.state = 'done'
      const secs = (task.elapsedMs / 1000).toFixed(1)
      const actions = [{ label: 'Copy path', run: () => void copyText(job.path).then(() => notify.flash('Path copied')) }]
      if (canReveal()) actions.push({ label: 'Show in folder', run: () => void revealPath(job.path).catch((e: unknown) => notify.error(e, 'Show in folder')) })
      notify.toast('success', `Exported ${task.rows.toLocaleString('en-US')} rows of ${job.label} to ${job.path} in ${secs} s`, 12000, actions)
    } catch (err) {
      if (ctrl.signal.aborted || isAbort(err)) {
        task.state = 'canceled'
        notify.flash(`Export of ${job.label} canceled; the partial file was removed`)
      } else {
        task.state = 'failed'
        task.error = errorMessage(err)
        notify.toast('error', `Export of ${job.label} failed: ${task.error}`)
      }
    } finally {
      task.controller = null
      this.#forget(task)
      this.#pump()
    }
  }

  #forget(task: ExportTask) {
    this.tasks = this.tasks.filter((t) => t !== task)
  }
}

export const exportTasks = new Exports()
