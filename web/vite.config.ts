import { defineConfig, type Plugin } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import { VitePWA } from "vite-plugin-pwa"
import { readFileSync, writeFileSync, existsSync } from "node:fs"
import { fileURLToPath } from "node:url"
import path from "node:path"

const root = path.dirname(fileURLToPath(import.meta.url))
// dev 代理目标：Go 后端（otd）默认 127.0.0.1:8080，可用 VITE_API_TARGET 覆盖。
const apiTarget = process.env.VITE_API_TARGET || "http://127.0.0.1:8080"

/**
 * 站点唯一的 Service Worker：src/sw/sw.js（WebTorrent 流 + 应用壳预缓存），由 vite-plugin-pwa（injectManifest）
 * 构建为 dist/sw.js（scope "/"）。dev 不启用 PWA：这里的中间件在 /sw.js 直接返回流媒体部分
 * （src/sw/webtorrent-sw.js）加上安装即接管，播放照常可用。另外保证 dist/.keep 在清空输出目录后仍然存在
 * （Go embed 需要该目录非空）。
 */
function devStreamingSW(): Plugin {
  return {
    name: "dev-streaming-sw",
    configureServer(server) {
      server.middlewares.use("/sw.js", (_req, res) => {
        res.setHeader("Content-Type", "text/javascript; charset=utf-8")
        res.setHeader("Cache-Control", "no-cache")
        res.end(
          readFileSync(path.join(root, "src/sw/webtorrent-sw.js"), "utf8") +
            `\nself.addEventListener("install", () => self.skipWaiting())\nself.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()))\n`,
        )
      })
    },
    closeBundle() {
      const keep = path.join(root, "dist/.keep")
      if (existsSync(path.join(root, "dist")) && !existsSync(keep)) writeFileSync(keep, "")
    },
  }
}

/**
 * JASSUB 的渲染器用 desynchronized: true 创建字幕画布（低延迟叠加平面）。在 Windows（DirectComposition）
 * 与 Android 的硬件视频叠加路径上，这样的画布会以不透明的黑色盖住下面的 <video>（只剩字幕可见）：
 * https://github.com/ThaUnknown/jassub/issues/70（上游不改）。构建时改为 false；
 * scripts/check-webtorrent-compat.mjs 检查构建产物里的 JASSUB worker 不再含 desynchronized: true。
 */
function jassubSynchronizedCanvas(): Plugin {
  return {
    name: "jassub-synchronized-canvas",
    enforce: "pre",
    transform(code, id) {
      if (!/[\\/]jassub[\\/]dist[\\/]worker[\\/]/.test(id) || !code.includes("desynchronized")) return null
      return { code: code.replace(/desynchronized:\s*true/g, "desynchronized: false"), map: null }
    },
  }
}

/**
 * 管理区的代码（src/pages/admin/**）只发给管理员：含这些模块的 chunk（及只由它们引入的资源）输出到
 * assets/admin/。服务器只把 assets/admin/* 发给已登录的管理员（其他人 404，
 * Cache-Control: private, no-store，见 internal/api/server.go），Service Worker 不预缓存它们（globIgnores）。
 * scripts/check-built-webtorrent.ts 检查其余产物里没有后台的接口路径 / 文案，预缓存清单里没有 assets/admin/。
 */
const ADMIN_SRC = path.join(root, "src/pages/admin") + path.sep
const isAdminModule = (id: string) => path.normalize(id.split("?")[0]).startsWith(ADMIN_SRC)

export default defineConfig({
  worker: { plugins: () => [jassubSynchronizedCanvas()] },
  plugins: [
    jassubSynchronizedCanvas(),
    react(),
    tailwindcss(),
    devStreamingSW(),
    VitePWA({
      strategies: "injectManifest",
      srcDir: "src/sw",
      filename: "sw.js",
      registerType: "prompt",
      injectRegister: null, // components/PWAUpdatePrompt.tsx（useRegisterSW）
      includeAssets: ["favicon.svg", "pwa-192.png", "pwa-512.png", "pwa-maskable-512.png", "apple-touch-icon.png"],
      manifest: {
        id: "/",
        name: "Project Kawaii",
        short_name: "Project Kawaii",
        description: "在浏览器中直接观看番剧与种子",
        lang: "zh-CN",
        start_url: "/",
        scope: "/",
        display: "standalone",
        background_color: "#ffffff",
        theme_color: "#4caf50",
        icons: [
          { src: "/pwa-192.png", sizes: "192x192", type: "image/png", purpose: "any" },
          { src: "/pwa-512.png", sizes: "512x512", type: "image/png", purpose: "any" },
          { src: "/pwa-maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
        ],
      },
      injectManifest: {
        rollupFormat: "iife", // 经典脚本：引擎与 useRegisterSW 注册的是同一个 /sw.js（type 一致）
        // 应用壳：页面代码、样式、图标。字幕渲染（JASSUB wasm / worker、CJK 字体）只在用到时下载，不预缓存
        globPatterns: ["**/*.{js,css,html,svg,png}"],
        globIgnores: ["**/jassub-worker*", "**/worker-*.js", "sw.js", "assets/admin/**"],
        maximumFileSizeToCacheInBytes: 3 * 1024 * 1024,
      },
      devOptions: { enabled: false },
    }),
  ],
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
    rolldownOptions: {
      output: {
        chunkFileNames: (c) => (c.moduleIds.some(isAdminModule) ? "assets/admin/[name]-[hash].js" : "assets/[name]-[hash].js"),
        assetFileNames: (a) =>
          a.originalFileNames.length > 0 && a.originalFileNames.every((f) => isAdminModule(path.resolve(root, f)))
            ? "assets/admin/[name]-[hash][extname]"
            : "assets/[name]-[hash][extname]",
      },
    },
  },
})
