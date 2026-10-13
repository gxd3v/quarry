import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [
    svelte(),
    {
      // The Go binary embeds dist/, so it must never be missing in a fresh clone.
      name: 'keep-dist',
      generateBundle() {
        this.emitFile({ type: 'asset', fileName: '.gitkeep', source: '' })
      },
    },
  ],
  // Assets are embedded in the binary and loaded locally, so bundle size matters little.
  build: { target: 'es2022', chunkSizeWarningLimit: 2000 },
})
