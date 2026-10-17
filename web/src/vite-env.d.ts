/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "1" 时启用 API/播放引擎 mock（仅开发） */
  readonly VITE_MOCK?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
