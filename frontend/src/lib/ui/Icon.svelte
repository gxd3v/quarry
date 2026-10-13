<script lang="ts" module>
  // 16px line icons drawn with the current color.
  const PATHS: Record<string, string> = {
    database: 'M3 4c0-1.1 2.2-2 5-2s5 .9 5 2v8c0 1.1-2.2 2-5 2s-5-.9-5-2zM3 4c0 1.1 2.2 2 5 2s5-.9 5-2M3 8c0 1.1 2.2 2 5 2s5-.9 5-2',
    schema: 'M2 3.5h5v4H2zM9 8.5h5v4H9zM4.5 7.5v3.5H9',
    folder: 'M2 4.5V12h12V6H8L6.5 4.5z',
    table: 'M2 3h12v10H2zM2 6.5h12M2 10h12M6.5 6.5V13',
    view: 'M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8zM8 6a2 2 0 1 0 0 4 2 2 0 0 0 0-4z',
    column: 'M5 2.5h6v11H5zM5 6h6',
    index: 'M3 3h2v10H3zM7 5h2v8H7zM11 8h2v5h-2z',
    function: 'M10.5 2.5C8 2.5 8 4 7.6 6.5L6.6 12c-.4 2-1.6 2-3.1 1.7M5 6.5h5',
    sequence: 'M3 4h2M3 8h4M3 12h6M11 3v10M9 11l2 2 2-2',
    key: 'M5.5 7a3 3 0 1 1 2.6 3L6 12H4.5v1.5H3V11l3.3-3.3A3 3 0 0 1 5.5 7z',
    connection: 'M6 2v3M10 2v3M4.5 5h7v3a3.5 3.5 0 0 1-7 0zM8 11.5V14',
    chevron: 'M6 4l4 4-4 4',
    play: 'M5 3.5v9l7-4.5z',
    stop: 'M4.5 4.5h7v7h-7z',
    plus: 'M8 3v10M3 8h10',
    close: 'M4 4l8 8M12 4l-8 8',
    refresh: 'M13 3.5V7H9.5M12.6 7A5 5 0 1 0 13 9',
    star: 'M8 2l1.8 3.8 4.2.5-3.1 2.9.8 4.1L8 11.3l-3.7 2 .8-4.1L2 6.3l4.2-.5z',
    search: 'M7 3a4 4 0 1 1 0 8 4 4 0 0 1 0-8zM10 10l3.5 3.5',
    edit: 'M10.5 2.5l3 3L6 13H3v-3z',
    trash: 'M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.7 9h5.6l.7-9',
    unplug: 'M4.5 5h7v3a3.5 3.5 0 0 1-7 0zM8 11.5V14M2.5 2.5l11 11',
    script: 'M4 2h6l3 3v9H4zM6.5 7.5h4M6.5 10h4',
    alert: 'M8 2l6.5 11h-13zM8 6.5v3M8 11.2v.3',
  }
</script>

<script lang="ts">
  interface Props {
    name: string
    size?: number
    fill?: boolean
  }

  let { name, size = 14, fill = false }: Props = $props()
</script>

<svg
  class="icon"
  width={size}
  height={size}
  viewBox="0 0 16 16"
  fill={fill ? 'currentColor' : 'none'}
  stroke="currentColor"
  stroke-width="1.3"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <path d={PATHS[name] ?? PATHS.folder} />
</svg>

<style>
  .icon {
    flex: none;
    display: block;
  }
</style>
