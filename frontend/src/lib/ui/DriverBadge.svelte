<script lang="ts" module>
  // Database engines as a two-letter mark in their own colour.
  const ENGINES: Record<string, { mark: string; color: string; name: string }> = {
    postgres: { mark: 'Pg', color: 'var(--blue)', name: 'PostgreSQL' },
    oracle: { mark: 'Or', color: 'var(--peach)', name: 'Oracle' },
    mongodb: { mark: 'Mo', color: 'var(--green)', name: 'MongoDB' },
    redis: { mark: 'Re', color: 'var(--red)', name: 'Redis' },
  }

  export function engineName(kind: string): string {
    return ENGINES[kind]?.name ?? kind
  }
</script>

<script lang="ts">
  interface Props {
    kind: string
    // Open connections show the mark filled.
    active?: boolean
    size?: number
  }

  let { kind, active = false, size = 18 }: Props = $props()
  const e = $derived(ENGINES[kind] ?? { mark: kind.slice(0, 2), color: 'var(--overlay2)', name: kind })
</script>

<span class="badge" class:active style:--c={e.color} style:--s="{size}px" title={e.name}>{e.mark}</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: var(--s);
    height: var(--s);
    border-radius: 5px;
    background: color-mix(in srgb, var(--c) 14%, transparent);
    color: color-mix(in srgb, var(--c) 80%, var(--text));
    font-size: calc(var(--s) * 0.5);
    font-weight: 700;
    letter-spacing: -0.02em;
    line-height: 1;
    transition:
      background 0.15s,
      color 0.15s;
  }

  .badge.active {
    background: var(--c);
    color: var(--crust);
  }
</style>
