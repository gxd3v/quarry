<script lang="ts">
  const WINDOW_MS = 500

  let fps = $state(0)
  let worst = $state(0)

  $effect(() => {
    let id = 0
    let frames = 0
    let maxGap = 0
    let start = performance.now()
    let last = start
    const tick = (now: number) => {
      frames++
      maxGap = Math.max(maxGap, now - last)
      last = now
      if (now - start >= WINDOW_MS) {
        fps = Math.round((frames * 1000) / (now - start))
        worst = Math.round(maxGap)
        frames = 0
        maxGap = 0
        start = now
      }
      id = requestAnimationFrame(tick)
    }
    id = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(id)
  })
</script>

<span class="fps" class:bad={fps < 50} title="frames per second; worst frame time in the last {WINDOW_MS} ms">
  {fps} fps · {worst} ms
</span>

<style>
  .fps {
    font-family: var(--font-mono);
    color: var(--green);
    min-width: 11ch;
    display: inline-block;
  }

  .bad {
    color: var(--peach);
  }
</style>
