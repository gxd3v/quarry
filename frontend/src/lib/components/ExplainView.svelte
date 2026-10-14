<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity'
  import type { PlanResult } from '../app/tabs.svelte'
  import { analyzed, flatten, fmtCount, fmtMs, hottest, type PlanLike, type PlanRow } from '../explain/plan'
  import Icon from '../ui/Icon.svelte'

  let { plan }: { plan: PlanResult } = $props()

  const collapsed = new SvelteSet<string>()
  let selected = $state('0')

  const root = $derived(plan.root as PlanLike | null)
  const timed = $derived(root ? analyzed(root) : false)
  const rows = $derived(root ? flatten(root, collapsed) : [])
  const hot = $derived(hottest(rows))
  const current = $derived(rows.find((r) => r.id === selected) ?? rows[0] ?? null)
  // Plan-wide figures (planning/execution time, settings) live on the root.
  const ROOT_KEYS = ['Planning Time', 'Execution Time', 'JIT', 'Settings']
  const header = $derived(root ? ROOT_KEYS.filter((k) => root.props[k]).map((k) => [k, root.props[k]]) : [])

  function toggle(r: PlanRow) {
    if (!r.hasChildren) return
    if (collapsed.has(r.id)) collapsed.delete(r.id)
    else collapsed.add(r.id)
  }

  function onkeydown(e: KeyboardEvent) {
    const i = rows.findIndex((r) => r.id === selected)
    if (e.key === 'ArrowDown' && i < rows.length - 1) selected = rows[i + 1].id
    else if (e.key === 'ArrowUp' && i > 0) selected = rows[i - 1].id
    else if ((e.key === 'ArrowLeft' && current && !collapsed.has(current.id)) || (e.key === 'ArrowRight' && current && collapsed.has(current.id))) toggle(current!)
    else return
    e.preventDefault()
  }

  function ratio(m: number): string {
    return m >= 1 ? `${fmtCount(Math.round(m))}× more rows than estimated` : `${fmtCount(Math.round(1 / m))}× fewer rows than estimated`
  }
</script>

<div class="explain">
  {#if plan.error}
    <p class="error">{plan.error}</p>
  {:else if root}
    <div class="head">
      <span class="kind">{plan.analyze ? 'Explain analyze' : 'Explain'}</span>
      {#each header as [k, v] (k)}<span class="fig" title={v}><span class="k">{k}</span> {v}</span>{/each}
      {#if plan.analyze && !timed}<span class="fig">this server reports no actual times</span>{/if}
    </div>
    <div class="split">
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="tree" role="tree" tabindex="0" {onkeydown}>
        <div class="row cols">
          <span class="op">Operation</span>
          <span class="num">{timed ? 'Self time' : 'Self cost'}</span>
          <span class="num">Est. rows</span>
          {#if timed}<span class="num">Rows</span><span class="num">Total</span>{:else}<span class="num">Total cost</span>{/if}
        </div>
        {#each rows as r (r.id)}
          {@const n = r.node}
          <div
            class="row"
            class:sel={r.id === current?.id}
            class:hot={hot.has(r.id)}
            class:never={!!n.props['Never executed']}
            role="treeitem"
            aria-selected={r.id === current?.id}
            aria-expanded={r.hasChildren ? !collapsed.has(r.id) : undefined}
            tabindex="-1"
            onclick={() => (selected = r.id)}
            ondblclick={() => toggle(r)}
            onkeydown={() => {}}
          >
            <span class="op" style:padding-left="{r.depth * 16 + 4}px">
              {#if r.hasChildren}
                <button class="twisty" class:open={!collapsed.has(r.id)} type="button" tabindex="-1" onclick={(e) => (e.stopPropagation(), toggle(r))}
                  ><Icon name="chevron" size={10} /></button
                >
              {:else}<span class="twisty"></span>{/if}
              <span class="name">{n.op}</span>
              {#if n.target}<span class="target">{n.target}</span>{/if}
              {#if r.misestimate}<span class="warn" title={ratio(r.misestimate)}>⚠ {r.misestimate >= 1 ? '×' : '÷'}{fmtCount(Math.round(r.misestimate >= 1 ? r.misestimate : 1 / r.misestimate))}</span>{/if}
            </span>
            <span class="num bar-cell">
              <span class="bar" style:width="calc((100% - 72px) * {r.share.toFixed(3)})"></span>
              <span class="val">{timed ? fmtMs(r.self) : fmtCount(r.self)}</span>
            </span>
            <span class="num">{fmtCount(n.rows)}</span>
            {#if timed}
              <span class="num">{fmtCount(n.actRows)}</span>
              <span class="num">{fmtMs(n.actual)}</span>
            {:else}
              <span class="num">{fmtCount(n.cost)}</span>
            {/if}
          </div>
        {/each}
      </div>
      {#if current}
        <aside class="props">
          <h3>{current.node.op}</h3>
          {#if current.node.target}<p class="target">{current.node.target}</p>{/if}
          <dl>
            <dt>Cost</dt><dd>{fmtCount(current.node.cost)}</dd>
            <dt>Estimated rows</dt><dd>{fmtCount(current.node.rows)}</dd>
            {#if timed}
              <dt>Actual rows</dt><dd>{fmtCount(current.node.actRows)}</dd>
              <dt>Time</dt><dd>{fmtMs(current.node.actual)} ({fmtMs(current.self)} own)</dd>
            {/if}
            {#if current.misestimate}<dt>Estimate</dt><dd class="warn">{ratio(current.misestimate)}</dd>{/if}
            {#each Object.entries(current.node.props).filter(([k]) => current.id !== '0' || !ROOT_KEYS.includes(k)) as [k, v] (k)}
              <dt>{k}</dt><dd>{v}</dd>
            {/each}
          </dl>
        </aside>
      {/if}
    </div>
  {/if}
</div>

<style>
  .explain {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }

  .error {
    margin: 12px;
    padding: 6px 10px;
    border-radius: var(--radius);
    color: var(--red);
    background: color-mix(in srgb, var(--red) 10%, transparent);
    white-space: pre-wrap;
  }

  .head {
    display: flex;
    flex-wrap: wrap;
    gap: 14px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    color: var(--text-2);
  }

  .kind {
    color: var(--accent);
    font-weight: 600;
  }

  .fig {
    max-width: 40ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .k {
    color: var(--muted);
  }

  .split {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    min-height: 0;
    flex: 1;
  }

  .tree {
    overflow: auto;
    outline: none;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .row {
    display: grid;
    grid-template-columns: minmax(280px, 1fr) 140px 80px 80px 90px;
    align-items: center;
    height: 24px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
    cursor: default;
  }

  .row.cols {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--bg-side);
    color: var(--text-2);
    font-family: var(--font-ui);
  }

  .row.sel {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
  }

  .row.never {
    color: var(--faint);
  }

  .row.hot .name {
    color: var(--peach);
    font-weight: 600;
  }

  .op {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
  }

  .target {
    color: var(--sapphire);
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .warn {
    color: var(--yellow);
  }

  .twisty {
    display: inline-flex;
    width: 14px;
    height: 14px;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: 0;
    background: none;
    color: var(--muted);
    cursor: pointer;
    flex: none;
  }

  .twisty.open :global(svg) {
    transform: rotate(90deg);
  }

  .num {
    text-align: right;
    padding-right: 10px;
  }

  .bar-cell {
    position: relative;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: flex-end;
  }

  .bar {
    position: absolute;
    left: 4px;
    top: 6px;
    bottom: 6px;
    border-radius: 2px;
    background: color-mix(in srgb, var(--peach) 45%, transparent);
  }

  .val {
    position: relative;
  }

  .props {
    overflow: auto;
    border-left: 1px solid var(--border);
    padding: 8px 12px;
    background: var(--bg-side);
  }

  .props h3 {
    margin: 0 0 2px;
    font-size: 13px;
  }

  .props .target {
    margin: 0 0 8px;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 10px;
    margin: 0;
  }

  dt {
    color: var(--text-2);
  }

  dd {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 12px;
    word-break: break-word;
    white-space: pre-wrap;
  }
</style>
