/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_QUARRY_URL?: string
  readonly VITE_QUARRY_TOKEN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
