# onlinetorrent (projectk.org)

在浏览器里直接看种子：后端把种子推送给 qBittorrent 5.3（原生 WebTorrent/WebRTC 做种），浏览器通过 wss tracker 找到它并用 WebTorrent 边下边播；qB 下载完成的文件还可以通过签名的 HTTP WebSeed 加速。首页是按播出时间排序的新番时间表（bangumi-data + 本地种子库），点击即可观看。种子由后台「抓取源」（bangumi.moe、蜜柑计划、Nyaa 等 RSS）定时抓取入库并刮削到番剧/集；搜索、条目种子列表都读本地库。

```
web/            React 19 + Vite + Tailwind v4 前端（构建产物嵌入 Go 二进制）
cmd/otd         主服务：API + 前端 + 后台
cmd/metad       独立的映射服务（/meta/v1），可单独部署
internal/
  store         主 SQLite：用户/会话/邀请码/兑换码/设置/qB 服务器/规则/裁决/审核/日志
  policy        策略引擎 policy.Decide(user, torrent) → allow / login / vip / block
  qb            qBittorrent WebAPI 客户端与服务器池（推送、补 wss tracker、清理、WebSeed 文件）
  catalog       时间表、种子列表（本地库）、bangumi.moe 解析、bgm.tv/TMDB 按需条目搜索
  crawl         抓取源调度（RSS/Atom、bangumi.moe）、.torrent 解析、qB 元数据队列、入库
  meta          映射库：作品 ↔ bgm/mal/anidb/tmdb…，文件级 (infohash, file_index) → 作品+集
  api           HTTP API（docs/api.md）
docs/           方案、API 契约、部署记录、调研、审查报告
```

## 开发

```bash
cd web && npm install && npm run build && cd ..
GOMAXPROCS=2 go build -o otd ./cmd/otd
./otd user -db otd.db -username admin -password '至少8位' -role admin
./otd serve -db otd.db -meta-db meta.db -addr 127.0.0.1:8080
# 前端热更新：cd web && npm run dev（代理到 :8080）；纯前端：npm run dev:mock
go test ./...
```

所有配置都在后台「站点设置」里（存 SQLite），启动参数只有 `-db -meta-db -addr`。qB 服务器在后台「qB 服务器」里添加；抓取源在后台「抓取源」（`/api/v1/admin/sources`），默认内置 bangumi.moe / 蜜柑 / Nyaa（启用）与动漫花园 / ACG.RIP（关闭）。只有磁力的条目会临时加到 qB 的 `<分类>-meta` 分类取元数据，取到后自动删除。

## 部署

推送到 `main`/`v2` 后 GitHub Actions 构建镜像 `darknightlab/onlinetorrent:<branch>`（Docker Hub + ghcr）。生产用 Portainer stack（见 `compose.yml`、`docs/deploy.md`），经 Cloudflare Tunnel 对外提供 https://www.projectk.org 。
