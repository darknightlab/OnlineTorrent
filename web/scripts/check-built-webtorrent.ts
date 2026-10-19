// postbuild：检查 Vite 实际输出的 WebTorrent chunk（生产构建会再压缩 webtorrent.min.js，
// 例如把 'request' 改成 `request`），与运行时用的是同一份代码。不兼容则构建失败并列出问题。
//   node scripts/check-built-webtorrent.ts [distDir]
import { readFileSync, readdirSync } from "node:fs"
import path from "node:path"
import { pathToFileURL } from "node:url"
import { checkWebTorrentBundle, installFetchRecorder } from "../src/player/store/bundle-check.ts"

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
console.log(`✓ WebTorrent 构建产物兼容（dist/assets/${chunk}）`)
process.exit(0) // 浏览器包在 node 里可能留下定时器
