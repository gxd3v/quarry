// A stream whose messages are already buffered is consumed in microtasks,
// which never let the browser render. slicer returns a function to await
// between messages: it yields a macrotask once the current slice ran for
// budgetMs, so long streams cannot stall the UI.
export function slicer(budgetMs = 8): () => Promise<void> {
  let start = performance.now()
  return async () => {
    if (performance.now() - start < budgetMs) return
    await macrotask()
    start = performance.now()
  }
}

const channel = typeof MessageChannel === 'undefined' ? null : new MessageChannel()
const waiting: (() => void)[] = []
if (channel) channel.port1.onmessage = () => waiting.shift()?.()

export function macrotask(): Promise<void> {
  return new Promise((resolve) => {
    if (!channel) {
      setTimeout(resolve, 0)
      return
    }
    waiting.push(resolve)
    channel.port2.postMessage(null)
  })
}
