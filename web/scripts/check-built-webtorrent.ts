// postbuild：检查 Vite 实际输出的 WebTorrent chunk（生产构建会再压缩 webtorrent.min.js，
// 例如把 'request' 改成 `request`），与运行时用的是同一份代码。不兼容则构建失败并列出问题。
//   node scripts/check-built-webtorrent.ts [distDir]
import { existsSync, readFileSync, readdirSync } from "node:fs"
import path from "node:path"
import { pathToFileURL } from "node:url"
import { checkServiceWorker, checkWebTorrentBundle, installFetchRecorder } from "../src/player/store/bundle-check.ts"

const dist = path.resolve(process.argv[2] ?? "dist")
const assets = path.join(dist, "assets")
const fail = (msg: string) => {
  console.error(`\n✖ WebTorrent 构建产物检查失败（${assets}）：\n${msg}\n`)
  process.exit(1)
}
const names = readdirSync(assets)
const chunks = names.filter((n) => /^webtorrent\.min-[\w-]+\.js$/.test(n))
if (chunks.length !== 1) fail(`  - 应有且只有一个 webtorrent.min-*.js chunk，实际：${JSON.stringify(chunks)}`)
const chunk = chunks[0]
// 引擎必须动态导入的正是这个 chunk
const engines = names.filter((n) => /^webtorrent-engine-[\w-]+\.js$/.test(n))
if (!engines.some((e) => readFileSync(path.join(assets, e), "utf8").includes(chunk))) fail(`  - webtorrent-engine-*.js 没有引用 ${chunk}`)

;(globalThis as { self?: unknown }).self ??= globalThis
const file = path.join(assets, chunk)
const fetchLog = installFetchRecorder() // before the import (the chunk captures self.fetch at load)
const { default: WebTorrent } = await import(pathToFileURL(file).href)
const problems = await checkWebTorrentBundle(WebTorrent, readFileSync(file, "utf8"), `dist/assets/${chunk}`, fetchLog)
if (problems.length) fail(problems.map((p) => `  - ${p}`).join("\n"))
// 站点唯一的 Service Worker（流 + 应用壳）
const swProblems = checkServiceWorker(readFileSync(path.join(dist, "sw.js"), "utf8"), readFileSync(file, "utf8"))
if (swProblems.length) fail(swProblems.map((p) => `  - ${p}`).join("\n"))
console.log("✓ Service Worker：流处理在前，/webtorrent/ 不经过 workbox，预缓存清单已注入")
// JASSUB 字幕画布：desynchronized 画布在 Windows / Android 的硬件视频叠加路径上会以黑色盖住视频（vite.config.ts）
const desync = names.filter((n) => n.endsWith(".js") && /desynchronized:\s*(?:!0|true)/.test(readFileSync(path.join(assets, n), "utf8")))
if (desync.length) fail(`  - 字幕画布仍为 desynchronized: true：${desync.join("、")}`)
if (!names.some((n) => n.endsWith(".js") && /desynchronized:\s*(?:!1|false)/.test(readFileSync(path.join(assets, n), "utf8"))))
  fail("  - 没有找到 JASSUB 渲染器（desynchronized: false），字幕画布补丁可能失效")
console.log("✓ 字幕画布：desynchronized 已关闭（不会盖黑视频）")
console.log(`✓ WebTorrent 构建产物兼容（dist/assets/${chunk}）`)

// 管理区只发给管理员（vite.config.ts、internal/api/server.go）：后台代码只在 assets/admin/，其余产物里没有后台的
// 接口路径与文案，Service Worker 不预缓存 assets/admin/。
const adminFail = (msg: string) => {
  console.error(`\n✖ 管理区隔离检查失败（${dist}）：\n${msg}\n`)
  process.exit(1)
}
const adminDir = path.join(assets, "admin")
const adminFiles = existsSync(adminDir) ? readdirSync(adminDir) : []
if (!adminFiles.some((n) => /^AdminApp-[\w-]+\.js$/.test(n))) adminFail("  - assets/admin/ 下没有 AdminApp-*.js（后台 chunk 没有单独输出）")
const adminCode = adminFiles.map((n) => readFileSync(path.join(adminDir, n), "utf8")).join("\n")
// 后台导航的文案（去掉「概览」「用户」「Tracker」这类通用词）加上几个后台接口路径片段
const GENERIC = new Set(["概览", "用户", "Tracker"])
const navLabels = [...readFileSync(path.resolve(import.meta.dirname, "../src/pages/admin/AdminApp.tsx"), "utf8").matchAll(/label: "([^"]+)"/g)].map((m) => m[1])
const adminOnly = [...navLabels.filter((l) => !GENERIC.has(l)), "qb-servers", "content-scopes", "rank-rules", "redeem-codes", "bgm-archive"]
const needles = [...adminOnly, "/api/v1/admin"]
const missing = adminOnly.filter((s) => !adminCode.includes(s))
if (missing.length) adminFail(`  - 后台 chunk 里没有这些字符串（检查本身已过时？）：${missing.join("、")}`)
const adminPath = /[`"']\/admin\/[a-z]/ // 接口路径 "/admin/stats" 等；chunk 地址 "assets/admin/…" 不算
const leaks: string[] = []
const walk = (dir: string) => {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) {
      if (p !== adminDir) walk(p)
      continue
    }
    if (!/\.(js|mjs|css|html|webmanifest|json)$/.test(e.name)) continue
    const text = readFileSync(p, "utf8")
    const rel = path.relative(dist, p)
    for (const s of needles) if (text.includes(s)) leaks.push(`${rel}: ${JSON.stringify(s)}`)
    if (adminPath.test(text)) leaks.push(`${rel}: ${text.match(adminPath)![0]}…`)
  }
}
walk(dist)
if (leaks.length) adminFail(leaks.map((l) => `  - 后台内容出现在 assets/admin/ 之外：${l}`).join("\n"))
if (readFileSync(path.join(dist, "sw.js"), "utf8").includes("assets/admin/")) adminFail("  - Service Worker 预缓存了 assets/admin/")
console.log(`✓ 管理区隔离：后台代码只在 assets/admin/（${adminFiles.length} 个文件），不预缓存`)
process.exit(0) // 浏览器包在 node 里可能留下定时器
