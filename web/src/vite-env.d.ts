/// <reference types="vite/client" />
/// <reference types="vite-plugin-pwa/react" />

interface ImportMetaEnv {
  /** "1" 时启用 API/播放引擎 mock（仅开发） */
  readonly VITE_MOCK?: string
  /** 构建标识（可选），写在 <html data-build> 上 */
  readonly VITE_BUILD_ID?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
