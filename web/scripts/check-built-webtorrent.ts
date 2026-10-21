// postbuild：检查 Vite 实际输出的 WebTorrent chunk（生产构建会再压缩 webtorrent.min.js，
// 例如把 'request' 改成 `request`），与运行时用的是同一份代码。不兼容则构建失败并列出问题。
//   node scripts/check-built-webtorrent.ts [distDir]
import { readFileSync, readdirSync } from "node:fs"
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
process.exit(0) // 浏览器包在 node 里可能留下定时器
