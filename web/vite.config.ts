import { defineConfig, type Plugin } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import { readFileSync, writeFileSync, existsSync } from "node:fs"
import { fileURLToPath } from "node:url"
import path from "node:path"

const root = path.dirname(fileURLToPath(import.meta.url))
// dev 代理目标：Go 后端（otd）默认 127.0.0.1:8080，可用 VITE_API_TARGET 覆盖。
const apiTarget = process.env.VITE_API_TARGET || "http://127.0.0.1:8080"

const swSource = path.join(root, "node_modules/webtorrent/dist/sw.min.js")

/**
 * WebTorrent 的 Service Worker 必须以 scope "/" 提供在 /sw.min.js。
 * dev: 中间件直接返回 node_modules 中的文件；build: 作为静态资源输出到 dist 根目录。
 * 另外保证 dist/.keep 在清空输出目录后仍然存在（Go embed 需要该目录非空）。
 */
function webtorrentSW(): Plugin {
  return {
    name: "webtorrent-sw",
    configureServer(server) {
      server.middlewares.use("/sw.min.js", (_req, res) => {
        res.setHeader("Content-Type", "text/javascript; charset=utf-8")
        res.setHeader("Service-Worker-Allowed", "/")
        res.setHeader("Cache-Control", "no-cache")
        res.end(readFileSync(swSource))
      })
    },
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "sw.min.js", source: readFileSync(swSource) })
    },
    closeBundle() {
      const keep = path.join(root, "dist/.keep")
      if (existsSync(path.join(root, "dist")) && !existsSync(keep)) writeFileSync(keep, "")
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), webtorrentSW()],
  resolve: { alias: { "@": path.join(root, "src") } },
  server: {
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
      "/meta": { target: apiTarget, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 600,
  },
})
