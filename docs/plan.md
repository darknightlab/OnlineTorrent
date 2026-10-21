# onlinetorrent v2 重构方案（草案，待确认）

调研资料：[qBittorrent/WebTorrent](research/qbittorrent-webtorrent.md) · [番剧数据源](research/anime-data-sources.md)

## 1. 旧系统回顾

| 部分 | 现状 |
| --- | --- |
| 前端 main | jQuery + MDUI1，单页：输入磁力 → POST 后端 → WebTorrent v2 播放；SW 流式、首尾块优先、200 段进度条、自动播放首个视频、bangumi.moe latest 列表（<2GB）、分享链接 `?uri=` |
| 前端 beta | 2022-12 的过时分支：多后端同源反代 `/server/N/`、webseed 实验，配合旧 Go(anacrolix) 后端 `/api/add/uri`。`beta.html/index-beta.js` 是其残留（webseed 默认开 + .torrent 上传）。**可废弃** |
| 后端 | TS/Express 单文件；YAML 配置 qB 列表；add 广播到所有在线 qB；定时清理做完种的；bangumi.moe 反代（socks）；无用户、无权限 |
| 部署 | 服务器是 **Podman** + Portainer stack（`/root/app/portainer/portainer_data/compose/N`），数据放 `/root/app/<app>`，端口绑 `192.168.233.8` 或 host 网络，cloudflared 隧道（远程管理）对外；tracker 已在 `onlinetorrent-tracker` stack（lili-tracker wss + aquatic-udp）。磁盘剩 4.6G |

## 2. 技术选型

### 后端：Go（推荐）
- 与你最近的 YotsubaNews 同栈（Go + ncruces/go-sqlite3 + 前端 embed + 单镜像），维护心智统一。
- 单静态二进制、镜像 ~20MB、内存小 —— 服务器磁盘/内存都紧张。
- 并发轮询多台 qB、看门狗（wss 卡死重 announce）、定时同步数据，Go 写起来最自然。
- 将来需要服务端自己做 WebRTC 种子时，`anacrolix/torrent` 原生支持 WebTorrent（pion）。
- 不选 TS：前端类型共享这点优势用 OpenAPI 生成即可弥补；不选 Rust：开发效率低、生态（qB 客户端/WebRTC 种子）不如 Go。

### 前端：React 19 + Vite + TypeScript + Tailwind v4 + shadcn/ui(Radix) + TanStack Query + React Router
- 同 YotsubaNews；shadcn 组件可控、无运行时主题锁死，便于做 **绿色 #4caf50 主色 + 蓝色强调** 与 light/dark/system 三态。
- 播放：`webtorrent@3`（ESM）+ 其 `sw.min.js` 流式；独立 `player` 模块封装（首尾块优先、分段进度条、peers/速度、webseed 回退）。
- PWA（可选）。

## 3. 架构

```
           Cloudflare (DNS/隧道)
                │
  ┌─────────────┴──────────────┐
  │ otd  (主服务, 单镜像)        │──── qBittorrent 5.3 池 (WebRTC 种子)
  │  - 前端静态 + /api          │
  │  - 用户/会话/角色            │──── 抓取源 (bangumi.moe / RSS，后台定时抓取入库)
  │  - 策略 policy.Decide()     │
  │  - qB 池/看门狗/清理         │
  │  - 后台管理 /admin          │
  │  SQLite: otd.db             │
  └─────────────┬──────────────┘
                │ HTTP
  ┌─────────────┴──────────────┐
  │ metad (映射服务, 可独立部署)  │──── bangumi-data / bgm.tv / Fribb anime-lists / AnimeGarden
  │  works / external_ids /     │
  │  episodes / torrent→episode │
  │  公共 API（以后 Torznab 给 Sonarr）│
  │  SQLite: meta.db            │
  └────────────────────────────┘
```
同一个 Go 仓库两个 `cmd/`，可以合并成一个进程跑（默认），也可分开部署。

### 关键模块
- **qB 池**：服务器配置存 SQLite（后台增删改），API Key(Bearer) 或会话复用；健康检查；`torrents/add` 后可选调 `torrents/addTrackers` 补 wss（每台 qB 一个开关，默认开，幂等无副作用）。背景：PR #24512 已合入 5.3.0rc1，目前会过滤磁力里的 ws/wss；这是白名单疏漏而非本意，预计上游会把 ws/wss 加入白名单，届时可在后台关闭该开关；分享限制；定时清理 `stoppedUP`。已知问题 qBittorrent#24899（长时间做种后 WebTorrent 停止响应）仅记录，暂不做对策，待实际环境观察。
- **策略（审核/权限）**：
  ```go
  type Decision struct { Action Allow|RequireLogin|RequireVIP|Block; Reason string; RuleID int64 }
  func (p *Policy) Decide(ctx, user *User, t TorrentInfo) Decision
  ```
  规则来源（按优先级）：管理员手工判定（infohash）> 屏蔽规则（关键词/正则/文件名/bangumi.moe tag/team/分类，标记 political/porn → 只要命中一律 Block，无视优先级）> 权限规则（某作品/某 tag 仅会员/大会员，按 priority）> 默认动作（后台可设）。没有元数据的种子不能播放（新 hash 先经 qB 元数据队列取元数据，见 api.md「resolve/events」），判定只用真实标题 / 文件，原来的「用户自填磁力最低权限」已删除。内容范围（按角色的条目标签范围）同样限制播放：已应用映射到隐藏条目的种子对该角色不可播放。前台理由只由 action+label 生成，不暴露规则名/裁决备注。所有拒绝写审计日志；未知/可疑的进入审核队列。实现见 `internal/policy` 包注释。
- **用户**：邮箱/用户名 + 密码（argon2id），HttpOnly 会话 Cookie；角色 guest / member / vip / admin；注册方式后台可切（开放 / 邀请码 / 关闭），Cloudflare Turnstile 防刷（可选）。
- **设置**：全部存 `settings` 表（KV + JSON），后台编辑，无 YAML。仅保留 `-db` `-addr` 两个启动参数。

### 前端页面
- **首页**：参考巴哈姆特动画疯 —— 顶部「今日/星期」时间线，按播出时间排序的番剧卡片（封面、标题、集数、更新时间、“已更新/即将播出”），点击直接进入最新一集播放；下方「最新种子」列表。
- **番剧页**：简介/封面（bgm.tv）、集数网格 → 每集可用种子（字幕组/分辨率/大小）。
- **播放页**：播放器 + 分段缓冲条、文件列表、速度/peers、分享、下载；手机上播放器置顶、信息折叠。
- **搜索 / 登录注册 / 个人中心**。
- **后台 /admin**：qB 服务器、站点设置、用户与角色、策略规则、审核队列、种子记录、映射纠错、审计日志。

## 4. 部署
- 镜像 `darknightlab/onlinetorrent`（GitHub Actions 构建，同 news）。
- Portainer 新 stack，数据 `/root/app/onlinetorrent:/data`，端口绑 `192.168.233.8:<port>`，cloudflared 隧道加 hostname。
- 直接部署到 `www.projectk.org`（用户已确认）；生产服务器只有 131.143.240.2。

## 已实现的补充（计划外）
- **HTTP WebSeed**（初版，已被边缘节点取代）：实测欧洲 qB → 国内浏览器的 WebRTC 只有 ~50KB/s（高 RTT 下 SCTP 窗口受限）。利用 qB 5.3 的 `torrents/downloadFile`（支持 Range），由 otd 提供签名的 BEP19 webseed（`/webseed/{exp}.{sig}/{ih}/`），经 Cloudflare 实测 ~9MB/s。后台可设：开关、最低用户等级（默认会员，可作为大会员权益）、延迟秒数（先 P2P 再 HTTP）。只服务 qB 已完整下载的文件。
- **WebSeed 边下边供 / 边缘节点（2026-10）**：qB 的 `torrents/downloadFile` 对未下完的文件一律返回 409（实测，qB 5.3.0beta1 / libtorrent 2.1.1）。曾尝试让 otd 以 BT 节点身份连 qB 读分块，热门种子受 qB 每种子连接上限 / 上传槽（choke）限制，已整体移除。现在的做法是**边缘节点**（`otd edge`，同一镜像）：跑在 qB 所在主机上，只读挂载 qB 的下载目录，直接向浏览器提供 WebSeed（含未完成文件 `.!qB` 中已校验的分块，整块 SHA-1 复核），经 Cloudflare 对外；它主动连本站的 WebSocket 隧道（`internal/tunnel`：多路复用、按流的窗口流控、大流量低优先级、25 s 心跳），隧道只走控制流量：qB WebUI 转发（`edge_api`，主站的 qB 客户端不变）、可用分块查询、公钥下发、状态统计——**数据不经主站**。鉴权用主站签发的短期 Ed25519 令牌（`internal/edgetoken`，按边缘节点 + 种子，不含用户身份），边缘节点离线验证。主站自己的 WebSeed（经 `downloadFile` 代理完整文件）已于 2026-10 整体移除：没有边缘节点的服务器不提供 WebSeed，`/webseed/…` 在主站上 404，`GET /webseed/hash/{ih}/{path...}` 只作为「可使用 WebSeed」的权限（不注册处理器，`perms.go` 的 `permOnly`），决定是否签发边缘节点令牌；设置 `webseed.progressive` 删除（迁移 9）。SSE 按 WebSeed 地址推送 `ready` / `seed` / `have` / `lost` / `complete` / `token` / `done`，浏览器按地址把 WebSeed 宣告的分块换成对应的 bitfield（webtorrent 内部接口，构建期 + 运行时兼容检查），并经 fetch 包装给边缘节点请求加令牌。前端订阅永不放弃（15 s → 5 min 退避）。代码：`internal/edge`（agent = 边缘节点，hub = 主站侧，disk = 读盘）、`internal/seedsrc`（`Source` = 已连接的边缘节点，只报告可用分块）、`internal/api/edge.go`。详见 api.md「HTTP WebSeed」、deploy.md「边缘节点」。
- **内封字幕（2026-10）**：MKV 的 ASS/SSA/SRT 轨道。自写的小型 Matroska 解析（`web/src/player/subs/`）：头部（Tracks、字体附件位置，必要时按 SeekHead）用有界的辅助读取；字幕块从 `<video>` 自己读取的字节流里取（webtorrent serveFile 的 `"iterator"` 事件替换迭代器，增量解析 Cluster，跳过音视频数据，拖动后在新位置找 Cluster 头重新同步），不额外下载。渲染用 JASSUB（libass WASM，OffscreenCanvas/WebGL，无 COOP/COEP 时单线程），只在要显示字幕时动态加载；字体附件只在第一次显示时读取（合计 > 64 MB 不加载）。默认轨道按 navigator.languages（简 / 繁 / 日 / 英…，同时看轨道名与语言代码，含 JPSC/JPTC 等字幕组写法），没有匹配的不显示；用户在「字幕」菜单里的选择本次会话内沿用。MP4 的 tx3g 未支持。端到端：`EXTRA=e2e/subtitles/checks.mjs MEDIA=<mkv> node e2e/edge/run.mjs`。
- **qB 只下载正在播放的文件（2026-10）**：`POST /torrents/hash/{ih}/seed {file}`；新种子用本地 .torrent 暂停添加（标签 `otd-pending`）→ 其余文件 `filePrio 0` → 开始（无 .torrent 时磁力 + `stopCondition=MetadataReceived`，元数据到了再设）；已有的种子只把新文件调回正常优先级，从不降低；大小上限按要下载的文件合计（`qb.Pool.Wanted`）。qB 的 `progress`/`size` 只计要下载的文件，清理规则不变即正确。边界分块中不下载文件的字节在 libtorrent 的 partfile（`.<v1>.parts`），边缘节点整块组装（文件 + partfile + 填充零）并校验后再切片（`internal/edge/partfile.go`）。v1+v2 混合种子在 qB 里以截短的 v2 哈希为 ID，qB 客户端自动换算（`internal/qb/ids.go`）。用真实 qbittorrent-nox 5.2.4 + libtorrent 2.0 验证（`web/e2e/fileselect/run.py`），并在生产 qB 5.3.0beta1 上用一个测试种子验证后删除。待改进：`server_complete`（服务器已缓存）现在表示「要下载的文件已下载完」，合集里其他集不一定在服务器上。
- **HTTP 元数据（磁力 `xs=`，2026-10）**：浏览器 WebTorrent 必须先从节点（ut_metadata）拿到元数据，WebSeed 和就绪推送也依赖它；校园网等对称 NAT / UDP 受限的用户与 qB 建不起 WebRTC 连接，页面停在「正在获取种子信息…」。现在服务器保存 .torrent 原文（映射库 `torrent_metainfo`，来源：抓取源 .torrent、qB `torrents/export`、bangumi.moe 按需下载），`GET /api/v1/torrents/hash/{ih}/torrent` 提供，play 返回的磁力带 `xs=` 指向它；未知磁力由前端在做种后轮询收录状态、拿到种子文件后重新添加。bangumi.moe：抓取只读 JSON，不预先下载 .torrent（每页 30+ 个，大多没人看），改为第一次被请求时按需下载并缓存。详见 api.md。
- ~~时间表自动选种优先 AVC/H.264，HEVC/10bit 降权~~ → 2026-10 起改为前端按后台「推荐规则」打分（见下方「Tracker 管理 / 做种统计 / 推荐规则」）。
- **浏览器分块缓存（2026-10）**：OPFS 每个 piece 一个文件（`ot-pieces/<ih>/<index>`，另存 meta.json 与 .torrent），预算 = min(`max_push_size_bytes`, 25% 配额)，QuotaExceededError 时收缩到已用的 90%。淘汰顺序：旧版 / 未知 OPFS 条目 → 不在播放的种子（整种 LRU）→ 播放中种子读位置之后最远的 piece → 读前窗口之外最远的 piece；读前窗口、首尾 piece、正在读写的 piece 不淘汰。替换 WebTorrent 的文件迭代器为窗口化读取（被淘汰的区间重读时按需重下），不发 HAVE_ALL + lt_donthave + 对缺失 piece 回 reject；离开播放页保留数据，再次打开用缓存的 .torrent 与启动 bitfield 秒开。多 tab 用 Web Locks，第二个 tab 用临时目录。依赖 WebTorrent 内部接口：`npm run build`/`npm test` 会跑 `src/player/store/webtorrent-compat.test.ts`，升级后不兼容直接构建失败；运行时检查不通过则播放器报错「播放器内核不兼容：…」（不回退）。端到端测试：`npm run e2e:chunkstore`。详见 research/chunk-store.md。

- **内容范围（2026-10）**：按等级（游客 / 会员 / 大会员；管理员不受限）限制可见的番剧条目，仿 YotsubaNews content scope：标签模式（全部 / 仅所选标签）+ 标签表达式（AND 优先于 OR）+ 排除标签，只看 works.tags ∪ meta_tags（不使用 nsfw 字段）。存于 otd.db（迁移 6：`role_content_scopes`、`role_tag_expression_terms`、`role_excluded_tags`），编译后缓存在内存（`internal/store/content_scope.go`、`internal/api/content_scope.go`）；SQL 谓词（json_each）用于搜索，内存 `Visible` 用于时间表 / 条目 / 种子状态 / play。后台页 `/admin/content-scope`。详见 api.md「后台」。

- **Tracker 管理 / 做种统计 / 推荐规则（2026-10）**：
  - tracker 由本站管理：设置 `webtorrent.trackers`（wss，公开）+ `bt.trackers`（udp/http）；推给 qB（seed、元数据队列）时 = wss + BT + 种子自带 announce（`torrent_metainfo.announce`），去重最多 30，并 addTrackers 补齐已在 qB 的种子；不再依赖 qB 的「自动添加 tracker」。
  - 做种统计 `internal/swarm`：UDP BEP 15 / HTTP / wss scrape，按 tracker 批量、全部并行、各 tracker 限速；**只按需**：API 返回的种子数据缺失或超过 30 分钟才查询，1.5 秒预算（1.2 秒时先写已响应部分），其余后台完成，in-flight 去重；不做全库定时查询（库里可能有几万种子）。计数跨 tracker 取最大值。Nyaa RSS 的 seeders/leechers 作初始值。存映射库 `swarm_stats`（迁移 8）。tracker 状态被动记录（最后一次实际使用的结果），没有主动探测。
  - 推荐：`internal/rank` 只定义与校验规则（`rank.rules`，/site 公开）；打分在前端 `web/src/lib/rank.ts`（依赖用户权限：游客看 WebRTC 节点与服务器缓存，会员还看 BT 做种者）；单集页按分排序、自动播放第一个。后台页「Tracker」「推荐规则」。默认值第二版（服务器缓存只作小加分、做种人数对数加分、简体 > 繁体 > 清晰度 > 小文件，关键词分组）见 [research/rank-rules.md](research/rank-rules.md)；库中未改过的第一版默认值启动时自动升级。
  - 待改进：tracker 状态只在内存（重启清空）；服务器缓存状态依赖 qB `torrents/info` 全量快照（1 分钟）；条目页 100 个种子首次打开要 scrape 100 个（单个 UDP 包 74 个，所以每个 tracker 2 个包）。

- **弹幕（2026-10）**：只取 Bilibili 与动画疯（巴哈姆特），由**未修改**的 huangxd-/danmu_api 作边车采集（AGPL；部署见 deploy.md「弹幕边车」，设置 `danmaku.api_url`，空 = 关闭）。不按标题搜索：bangumi 条目 → bangumi-data 的平台 ID（`bd_sites`，导入全部年份）→ 平台分集列表 → 我们的官方集数（只在确定时，规则见 api.md「GET …/danmaku」）→ 平台分集 → danmu_api。只在有人打开该集（弹幕请求）时采集，从不批量；保存到映射库 `danmaku`（每集每平台：时间、模式、颜色、文字，gzip；平台分集 ID、采集时间），一集有了弹幕就不再采集；空 / 失败不保存，按集退避重试；同一集并发请求共用一次采集（singleflight）。代码 `internal/danmaku`、`internal/meta/danmaku.go`、`internal/api/danmaku.go`。前端：单集页（正在播放映射到本集的文件时）请求弹幕并懒加载 artplayer-plugin-danmuku（单独 chunk；插件自带的发送框 / 设置面板不用）；弹幕层在视频之上、内封字幕画布（z 35）之下；`/watch/hash` 页没有弹幕。
  - **发送弹幕（2026-10）**：`POST …/episodes/{n}/danmaku`（需登录，默认会员及以上），存映射库 `user_danmaku`（迁移 14），GET 时与采集的弹幕合并（每次现读；平台没有的集也有弹幕）。服务端校验 1–100 字、无控制字符、时间在视频时长内；每用户每 3 秒 1 条、每 10 分钟 30 条。
  - **弹幕栏**：单集页播放器与标题之间一行（桌面与手机相同，手机紧凑）：「弹幕」按钮（打开设置：显示 / 不透明度 / 字号 / 速度 / 显示区域 1/4、1/2、3/4、全屏，每个浏览器保存在 localStorage `ot.danmaku.settings`；默认显示、3/4）+ 发送框（回车发送，空 / 超长禁用，错误在下方显示几秒）；游客显示「登录以使用完整功能」。发送成功后马上出现在播放器里（带边框；接口返回的自己发的弹幕也带边框）。控制栏不再有「弹幕」按钮。全屏时看不到弹幕栏，弹幕照样按设置显示。
  - 覆盖（开发库 2026-10-21）：bangumi 条目有 Bilibili / 动画疯 ID 的 4266 个（Bilibili 3379、动画疯 1739）；2023–2026 每年约 190–200 部 TV/WEB 有平台 ID。当前时间表 140 部有平台 ID（本季全部只在动画疯，bangumi-data 没有 Bilibili）：第 1 集可确定映射 134 部，最新已播集 127 部；2023 年以前随机 60 部（都有 ID）：Bilibili 57 部中 40 部可确定（另有 10 部当时被 Bilibili 风控 412 拒绝），动画疯 14 部中 9 部。不能确定的常见原因：动画疯一页里包含上一季 / 下一部分的集、台湾限定作品超过 12 集（acgDetail 页只列前 12 集，API 在台湾以外拒绝）、同一部作品多个 Bilibili 列表都不完整。
  - 待定 / 待改进：播出当天第一次打开就采集，之后不再更新（新番弹幕会很少，可考虑播出后若干天才采集）；Bilibili 只拿到当前弹幕池（数千条，不是全部历史）；Bilibili 风控（-352 / 412）时失败退避重试；退避状态只在内存。

## TODO（低优先级）
- **边缘节点 v2**：
  - 浏览器 → 边缘节点目前经 Cloudflare；同一局域网 / 同运营商的用户可考虑直连（需证书与入站端口），按网络选择地址。
  - 将来允许用户在家里运行边缘节点（为此令牌里没有用户身份）：不再给完整的 qB WebUI 转发，改为受限命令集（添加 / 删除本站分类的种子、查询状态）；节点主人的配额与允许列表（哪些种子、多少流量）；按节点记录可靠性（失败率、哈希校验失败次数 → 自动停用）；经隧道推送的令牌吊销列表（按 `sid` / 角色）。
  - 分块读取的 LRU（同一分块被多个观众请求时只读盘、校验一次）。
- 浏览器分块缓存按稀有度淘汰：同等距离下优先淘汰 swarm 里副本多的 piece（`_rarityMap` 作次级排序键，见 web/src/player/store/policy.ts `pickVictim`）
- dandanplay 弹幕（需 AppId/Secret，用户暂无账号；dandan episodeId = AniDB aid × 10000 + 集数，可经 bangumi-data 的 anidb ID 确定映射）。现有弹幕见「已实现的补充」→「弹幕」
- 番剧页按集分组（映射库已有文件级集数，前端目前是种子列表 + 字幕组筛选）
- ✅ 特别篇（2026-10）：集的键 `12` / `12.5` / `0` / `sp1` / `ncop1` / `nced1` / `pv1` / `cm1`（api.md「集的键」），选集列表 `GET /subjects/{p}/{id}/episodes`，SP/NCOP/NCED/PV/CM 保留编号并映射到作品（迁移 5 + 启动时一次性重新分类）。待改进：SPs 目录里文件名解析不出编号的特别篇都记为 sp1；日文写法（ノンテロップOP/ED、映像特典内的 PV）未识别。
- 映射纠错的后台界面（API `PUT /meta/v1/torrents/{ih}/files/{idx}` 已有）
- 封面图自建缓存/缩略图（目前直链 lain.bgm.tv / bangumi.moe）
- **本集截图**：外部站点（AniList streamingEpisodes、Kitsu、TMDB still、TVDB）对当季新番的分集图要么没有、要么滞后，AniDB/bgm.tv 没有分集图。方案：qB 下载完成后，后端用 ffmpeg 经 `torrents/downloadFile`（Range）在 ~40% 处截帧（多帧取亮度正常者），存 WebP，按 (作品, 集) 存入映射库；时间表卡片优先显示本集截图。代价：镜像需加 ffmpeg。
- **时间表相邻日封面预加载**：7 天面板 DOM 已一次性渲染，但封面是原生懒加载，首次滑到某天时图片才加载。可让当天及相邻两天 eager 加载，其余在空闲时预加载（多约 1–2MB 流量）。
- **16:9 横图来源**：目前卡片为 16:9，图片用竖版封面宽度铺满、偏上裁切（object-position 50% 30%）。可选补充：TMDB backdrop（16:9，需免费 API key；bangumi-data 本季仅约 1/3 带 tmdb id，可再按标题搜索补全）；AniList bannerImage 为 1900×400（约 4.75:1，本季覆盖约 60%），裁 16:9 效果一般，不建议。
- Torznab 端点给 Sonarr
- **qB 种子保留策略（规则化）**：目前只有每台 qB 的「做种时间上限 / 分享率上限」可在后台设置；`Pool.Cleanup` 写死为「达到上限停止 → 连文件删除」「48 h 未下完 / 无元数据 → 删除」。游客不能触发做种，文件被删后只能等会员来播。以后做成可组合的规则：按字幕组 / 来源 / 作品 / 单个种子匹配（合作字幕组可长期保留）；动作包括做种时长、有人播放即续期、到期只停止不删文件、磁盘配额与按最久未播放淘汰、下载超时；是否允许游客播放触发「恢复已删除的服务器缓存」也作为策略项。
- **推流直播（P2P 分流，可接受 10–30 s 延迟）**：HLS 分段 + p2p-media-loader（Apache-2.0，PeerTube 同款）+ 现有 wss tracker（原型已连通，见 `docs/research/webtorrent-live.md`）；前端经 hls.js 接入 ArtPlayer；播放列表与分段走 Cookie 鉴权与接口权限，P2P 侧登录后下发不可猜的频道 ID、分段按服务器给的哈希校验；锁定 p2p-media-loader 大版本；iOS 17.1 以下退回纯 HLS。待定：推流来源与转码（HEVC 10bit / ASS 字幕）、并发规模与上行带宽、权限模型。
- qBittorrent#24899 观察，如复现再做 reannounce 看门狗
- 向 qBittorrent 上游反馈 #24512 过滤 wss 的副作用

## 5. 已确认
单仓库；直接用 www；注册支持邀请码/邀请链接；只有一台生产服务器；弹幕（2026-10 已做，见上）。

## （原）待确认
1. 仓库：新建一个 monorepo（建议 `darknightlab/onlinetorrent` 新开 `v2` 分支或新仓库），还是继续前后端两个仓库？
2. 预发域名用 `next.projectk.org` 可以吗？
3. 大会员怎么来：先做“管理员授予 + 兑换码”，支付以后再说？
4. 注册：开放注册 + Turnstile，还是邀请制？是否需要邮箱验证（需要 SMTP）？
5. 生产用哪些 qB（PKU/ZJ/MCI 是否会升 5.3）？测试先只用 ams。
6. 弹幕需要 dandanplay AppId/Secret（以后再申请也行）。

## 抓取与刮削（2026-10 设计，取代对 bangumi.moe 的实时反代）

> 状态（后端）：1–4 已实现（`internal/crawl`、`internal/store/sources.go`、`internal/meta/catalog.go`、`internal/catalog/external.go`；接口见 [api.md](api.md)「搜索」「抓取源」「GET /torrents/hash/{ih}」）。前端已完成：搜索页（番剧卡片 + 种子列表）、单集页指定资源 `/subject/{p}/{id}/episode/{key}/hash/{ih}[/file/{idx}]`、播放页做种后轮询并无缝切到单集页（`web/src/pages/watch/Playback.tsx`：播放页与单集页路由共用同一组件，Player 以 infohash 为 key，不重建会话）、后台「抓取源」页（含元数据队列）。

原则：不再在用户请求路径上实时请求 bangumi.moe（搜索 / 条目种子 / 时间表最新一集全部读本地库）。种子来自后台抓取源与用户提交的磁力。暂不做历史统一补抓。

### 1. 抓取源（admin 可配，存 SQLite）✅
`sources(id, name, kind, url, interval_min, enabled, etag, last_modified, last_run_at, last_ok_at, last_new, last_error)`
- kind：`rss`（通用 RSS/Atom：enclosure .torrent / magnet / nyaa:infoHash）、`bangumi_moe`（其 JSON latest 第 1 页，含标签 ID 与文件列表，作为强刮削线索）。
- 所有抓取源的种子都视为资源站种子（无用户磁力门槛；只有用户自行粘贴的未知磁力有「至少登录」门槛）。
- 默认：bangumi.moe（启用）、蜜柑 Mikan RSS（启用）、Nyaa 1_3（启用）、动漫花园、ACG.RIP（内置但关闭）。间隔默认 15 分钟，最小 5。
- 只抓第一页；条件 GET（ETag / If-Modified-Since）；每个 host 串行；UA 标明站点。
- 后台：列表、增改删、启停、「立即抓取」、显示上次结果/错误。✅ 接口 `/api/v1/admin/sources…`（另有 `schema`、`probe` 测试，同 YotsubaNews）；前端页面待做。
- 实现补充：连续失败指数退避（interval × 2^n，最多 6 小时）；已处理条目记在 `source_items`（30 天），不会重复下载 .torrent；只有全部条目处理完才记住 ETag；直连时只允许公网地址（.torrent 链接来自第三方 Feed，防 SSRF）；`upstream.proxy` 为空时走环境变量代理。

### 2. 入库流水线（每个 infohash 只做一次）✅
RSS 条目 → infohash + 文件列表：
1. 有 .torrent（enclosure/link）：下载（≤5MB、限速）本地解析 bencode → infohash（v1）+ 文件列表。
2. 只有磁力：进入「qB 元数据队列」：qB 添加时 `stopCondition=MetadataReceived`、分类 `projectk-meta`，轮询文件列表，取到后删除（若该种子同时被做种请求则保留）。并发 3、单个超时 2 分钟，失败重试 3 次；状态存库，重启续跑。
3. 文件分类：视频扩展名；排除 NCOP/NCED/PV/CM/Menu/Sample/特典；集数解析；kind = episode|sp|op|ed|extra|other。
4. 刮削：沿用 meta.match（bangumi.moe 标签 → 强匹配；标题/别名模糊匹配），写 torrent_files(work_key, episode, confidence, method)。低可信度进入后台待确认（TODO UI）。
   - 已实现：torrent_files 现在保存全部文件（非视频 kind=other）；`meta.ClassifyFile` 处理 SPs/特典 目录与 Sample；所有抓取源的种子均受信（取代原来的「Source == bangumi.moe」判断；源上不再有 trusted 开关）（otd.db torrents.trusted、meta.db torrents.trusted）；会员 seed 后由做种检查（postCheck + 10 分钟复查）入库刮削；元数据队列的种子被做种请求时转入做种分类（`qb.Pool.Push` → adoptMeta）。
   - 待改进：标题/别名匹配命中率一般（如 Skymoon/ANi 的罗马音标题），可加 FTS 候选 + 相似度打分。→ 2026-10：规则只映射确定的文件，其余由决策模型 Clef 在 FTS 候选中选择（见「映射关系」→「规则收紧 + 决策模型 Clef」）。
RSS 信息（标题、发布时间、字幕组、来源条目 ID）作补充；文件列表以种子为准。

### 3. 搜索（本地优先 + 外部按需）✅（旧 `GET /api/v1/torrents/search` 已删除）
`GET /api/v1/search/{keyword}` → `{ subjects: Subject[], torrents: Torrent[], page, page_count }`
- subjects：本地 works（标题/中文名/别名，FTS5 trigram）。本地无结果时按需查外部：bangumi.tv 搜索 API（type=动画），TMDB（设置了 key 时），结果写入 works 后返回；负结果缓存 1 小时；外部请求限速。
- torrents：本地 torrents 标题 + 文件名 FTS5（trigram，中日文可搜）。
- 磁力/infohash 不走搜索：前端直接进入 /watch/hash/{ih}。

### 4. 用户提交的磁力 ✅（后端：`GET /api/v1/torrents/hash/{ih}`；前端轮询与切换 ✅）
- 游客：只在浏览器 WebTorrent 播放（现状）。
- 会员（有做种权限）：播放同时 `POST …/seed` 让 qB 下载；后端发现 qB 拿到元数据即入库 + 刮削。
  前端轮询 `GET /api/v1/torrents/hash/{ih}`（`{status: pending|ready, files: [{index,path,size,kind,subject?,episode?}]}`），
  若播放中的文件被刮削到某番某集 → 切到 `/subject/{p}/{id}/episode/{key}/hash/{ih}/file/{idx}`（单集页指定资源，播放器不重启、保留进度）；否则留在 /watch。

### 5. 页面
- 单集页支持指定资源：`/subject/{p}/{id}/episode/{key}/hash/{ih}[/file/{idx}]`。
- 搜索页：「番剧」卡片区 + 「种子」列表区。
- 后台新增「抓取源」页。

### 以后（TODO）
- 统一历史补抓；TVDB / AniDB（AniDB 用其 titles dump，遵守限流）。
- 内置 BT 客户端 / tracker（统计用户上传量）。

## bangumi Archive 导入（bgm.tv 官方每周数据 dump）

来源：<https://github.com/bangumi/Archive>（每周三 05:00 北京时间更新；官方说明「方便不需要实时数据的场景，减少爬虫」）。
最新文件：`https://raw.githubusercontent.com/bangumi/Archive/master/aux/latest.json` → `{browser_download_url, size, digest(sha256), created_at}`。

### dump 结构（2026-10-13 实测）
zip 内 9 个 jsonlines 文件，只用 3 个：

| 文件 | 压缩 | 解压 | 用途 |
|---|---|---|---|
| `subject.jsonlines` | 290 MB | 987 MB | 条目（`type=2` 为动画） |
| `episode.jsonlines` | 67 MB | 340 MB | 章节 |
| `subject-relations.jsonlines` | 6 MB | 75 MB | 条目关联（定位多季合集的第 N 季） |

字段：
- subject：`id, type, name, name_cn, infobox(wiki 原文), platform, summary, nsfw, date, tags, score, rank, meta_tags…`。infobox 里有 `别名={[…][…]}`、`话数`、`放送开始`、`放送星期`、`播放结束`、`官方网站`。
- episode：`id, subject_id, sort, type(0 正片/1 SP/2 OP/3 ED/4 PV/5 MAD/6 其他), name, name_cn, airdate, duration, disc, description`。
- subject-relations：`subject_id, relation_type, related_subject_id, order`（类型码见 bangumi/common：2 前传、3 续集、4 总集篇、5 全集、6 番外篇…）。

### 下载：HTTP Range 流式读取，不落盘
GitHub release 支持 Range（不支持后缀式 `bytes=-N`，要用绝对区间）。
1. 读 `latest.json`；`digest` 与上次相同则跳过。
2. Range 取 zip 末尾 64 KB → 解析中央目录，得到各文件的本地头偏移和压缩大小（兼容 zip64）。
3. 对 3 个需要的文件各发一个 Range 请求 → 跳过本地头 → `flate` 流式解压 → 按行解析 JSON（行可能很长，用 `bufio.Reader`，不用默认 Scanner）。
4. 总下载约 363 MB/周，内存恒定，不占磁盘。最后校验整体 sha256 不可行（只下载部分），改为校验每个文件的 CRC32（中央目录里有）。

### 导入（写入映射库 meta.db）
- **条目**：只保留 `type=2`。键 `bgm:{id}`，已有作品（来自 bangumi-data）就合并，否则新建，分配新的本站数字 ID（已有 ID 不变）。
  - 合并优先级：放送时间、各平台外部 ID 以 bangumi-data 为准；中文名、简介、别名（取并集）、平台类型、NSFW（独立列 `nsfw`）、标签、评分、排名、开播/完结日期以 dump 为准。
  - infobox 用 `github.com/bangumi/wiki-parser-go` 解析。
  - 封面：dump 不含图片，沿用现有方式（访问时按需解析 lain.bgm.tv 地址并缓存）。
- **章节**：新表 `episodes(work_key, bgm_ep_id, type, sort, name, name_cn, airdate, duration)`；不存长简介（省空间）。类型映射到集标识：0→正片 `{sort}`、1→`sp{sort}`、2→`ncop{sort}`、3→`nced{sort}`、4→`pv{sort}`；5 MAD / 6 其他暂不进选集。
  - 注意：分割放送的第二季，bangumi 的 `sort` 可能延续第一季（13–24），而字幕组文件写 01–12。刮削时：文件集数 n 在该条目正片 `sort` 里存在就直接对应，否则按「该条目最小 sort + n − 1」换算。
- **关联**：新表 `subject_relations(subject_bgm_id, related_bgm_id, relation_type, ord)`，两端都是动画才保存。不做「系列」实体（跟随 bangumi：每季一个条目）；关联只用于刮削时沿 前传/续集 定位多季合集里的第 N 季。
- 批量写入：每 5000 行一个事务；写完后重建全文索引（`INSERT INTO works_fts(works_fts) VALUES('rebuild')`）。
- 预计体积：动画条目约 3 万、章节约 30–40 万行，映射库增加约 50–150 MB（导入后实测再更新）。

### 调度与后台
- 每周三 06:00（北京时间）自动同步一次；启动时若从未同步过则立即同步。
- 设置：`bgm_archive.enabled`（默认开）。
- 后台「数据同步」：上次同步时间、dump 日期、digest、条目/章节/关联数量、耗时、错误；「立即同步」按钮（运行中返回 409）。

### 同步后可以改进的地方
- 选集列表改用官方章节数据（含 SP/OP/ED/PV），并显示每集标题、播出日期；无资源的集置灰。
- 搜索的番剧结果全部来自本地（bgm.tv 实时搜索只作最后兜底）。
- 别名大幅增加，刮削（尤其罗马音标题）命中率提高。
- 条目页显示标签、评分、排名。
- NSFW：dump 里是条目的独立布尔字段 `nsfw`，导入时原样存入 works 的独立列 `nsfw`（与标签分开）。默认不做任何处理，后续再决定是否作为策略规则字段使用。

### 实施状态（2026-10-18）
- 已实现：`internal/meta/bgmarchive_zip.go`（HTTP Range 读 zip，zip64、CRC32 校验）、`bgmarchive.go`（导入）、`episodes_bgm.go`（官方章节、已播集数、分割放送换算，读取侧换算）、`internal/catalog/archive.go`（每 6 小时检查 latest.json）、后台 `GET/POST /api/v1/admin/bgm-archive[/run]`、设置 `bgm_archive.enabled`。
- 开发机实测（dump-2026-10-13）：85 秒、内存约 45 MB；动画条目 31174、章节 347689、关联 29893；meta.db 5.7 MB → 89 MB。
- 合并：别名分来源存（bd_aliases / bgm_aliases）后取并集；中文名、简介以 bangumi 为准；放送时间、类型、日期以 bangumi-data 为准；本站数字 ID 不变。
- bangumi-data 导入不限年份（2026-10；原来只导入近 3 年）：全部条目的标题、各语言译名（zh-Hans / zh-Hant / en / ja，作别名）、放送、类型、日期与平台 ID 都导入，旧作品也有 bangumi-data 的英文名（决策模型的名称核对用）。时间表只按日期窗口取候选（`WorksForSchedule`），不受影响。开发库实测：作品 31230 → 31296（新增 62 个无 bangumi ID 的 `bd:` 条目等），来源为 bangumi-data 的 1106 → 8910，时间表候选 233 → 233。注意 bangumi-data 的英文名并不全：魔法禁书目录（2008）没有英文 / 罗马字名，点兔 BLOOM 只有 "Is the order a rabbit ? BLOOM"，这两例的名称核对仍不通过。

## 映射关系（2026-10-18 确认）
不做「系列（series）」：跟随 bangumi，每季 / 每部剧场版 / OVA 都是独立条目（subject）。
- 一个条目对应多个种子；一个种子可以对应多个条目（多季合集、正片 + 剧场版合集）。
- 种子内的每个文件最多对应一集（集属于某个条目）；一集可以对应多个文件。
- 结构已满足（`torrent_files` 按文件存 `work_key` + 集）。
- ✅ 已实现（`internal/meta/route.go`）：种子级匹配（bangumi.moe 标签 → 近期别名 → 标题片段精确匹配任意年代条目，片段里的年份须与开播年相符）得到锚点条目；每个视频文件从文件名到外层文件夹逐级判断：剧场版/OVA 关键词 → 关联条目中同类型的（多个时按独有标题、年份区分）；季号 → 沿 前传/续集（2/3）排出的 TV/WEB 季链第 N 个（锚点自身标题含季号时按它定位）；关联条目的独有标题 → 该条目；否则锚点。分组后按（条目, 类型, 集）做冲突检测；剧场版/OVA 文件转为正片，剧场版唯一视频为第 1 集，无集号的按官方章节标题/播出日期对上；非锚点分组用官方章节数核对，不合理则不映射。一次性重刮标志 `reclassify_match_v3`。

### 规则收紧 + 决策模型（2026-10）
规则只在**确定**时直接映射，其余文件留空并交给决策模型。后端可选（设置 `clef.provider`）：**`openai`（默认）** = 兼容 OpenAI 的大模型 + 结构化输出（`gpt-6-luna`，主人的网关 `https://sub2api.projectk.org/v1`）；`clef` = Cloudflare Workers AI 的 Clef（`@cf/cloudflare/clef-flash`，只输出我们给定选项的概率，不生成文本；免费额度小、准确率较低，保留作备选）。数据库里已有的映射不重新处理（`Reclassify` 只用确定的结果改写旧行，不确定时保留原值；一次性重刮标志未升级）。

**确定**（`internal/meta`：`resolveWork` → `route` → `mapFiles`）= 同时满足：
1. 锚点确定：bangumi.moe 标签；或标题片段（normName 后）**精确等于**恰好一个条目的标题 / 中文名 / 别名（得分最高且有只属于它的片段，年份须相符），且标题里的季号与该条目自身标题的季号一致（S1 也可对无季号条目）。
   - **系列共用名（2026-10）**：标题片段去掉季号后（`franchiseBase`：S3、Ⅲ / III、3rd Season、第3期、Dai San Ki、第弐幕、末尾数字；不去括号里的「(2012)」之类）等于 **2 个及以上** TV/WEB 条目的名字（如 S1 与 Ⅲ 都叫 "Toaru Majutsu no Index"）时：种子的季（标题的季号，否则所有视频一致的季号，都没有 = 第 1 季）按**每个条目自己标题的季号**（无季号 = 第 1 季）对上恰好一个 → 该条目，确定；标题不能决定时（条目标题都没有季号：篇章副标题、The Final Season 等）按**开播先后**排序，第 N 个 = 第 N 季（同一天开播、或该条目自身季号不是 N → 不确定）。不用 bgm 前传/续集方向（那是故事顺序，种子的 S2 是播出顺序）。按开播顺序取、或标题没写季号时，发布时间不得早于该条目开播 7 天以上；集数仍须在该条目官方章节里。各片段命中的条目集合必须相同（「Keroro / KERORO 軍曹☆」是两部作品）；某个片段是共用名 + 副标题（「JOJO的奇妙冒险 飙马野郎」）→ 不确定。只有一个条目时不走这条（「Ace of Diamond Act II Season 2」不会因 actⅡ 的 Ⅱ 被当成第 2 季）。
   - 名称索引（2026-10）：所有条目（不再限近 18 个月）的标题 / 中文名 / 别名放在内存索引里（`aliasIndex`：原样小写名 → 条目，用于不确定的「别名包含」猜测；`normName` 与 `franchiseBase` → 条目，用于上面两种精确匹配，代替每个片段一次 works_fts 查询）。开发库 3.1 万条目：首次构建约 0.9 s、约 40 MB；之后每 10 分钟在后台重建，旧索引继续服务；新增条目时作废重建。每个种子 resolveWork 约 1 ms（原来 FTS 约 9–13 ms），Match 中位数约 5 ms。
2. 文件自己的名字（文件名与各级目录的标题片段，去掉开头的字幕组括号）没有**精确**点名另一部作品（名称索引查找，不查库）：点名的作品不是目标条目、不在其关联条目里、不与它共用系列名、也不是它自己名字的一部分（「Dragon Ball Super」之于「Dragon Ball Super: Beerus」）→ 该文件不按规则映射，交给决策模型（例：「[GM-Team][时光代理人 第3季]」里的「[灵境行者][Spirit Realm Walker][08]」文件）。没有作品名的文件（「03.mkv」）或用该作品任一名字 / 别名的文件照常。每个文件约 30 µs。
2. 文件留在锚点上、没有相反线索：文件名 / 目录的季号就是锚点的季（或 S1 + 锚点是季链第一个）；剧场版 / OVA 关键词与锚点类型一致（或没有该类型的关联条目）；不含其他关联条目的独有标题。
3. 集数确定（`ParsedName.Sure`）：显式标记（S01E02 / EP02 / 第2话）、` - 02`、单独括号 `[02]`、特别篇自带编号（SP02、NCOP2）。
4. 集的键**按文件原样**在该条目官方章节里（`officialSet.valid`：该类型有官方章节 → 必须在列表中；无正片章节 → 0 < n ≤ 话数；该类特别篇无官方章节（OP/ED/PV/CM 常见）→ 接受）。

**改为不确定（交给模型）的规则路径**（之前直接映射）：
| 位置 | 原规则 |
|---|---|
| `match.go` `resolveWork` | 近期作品别名**子串**匹配（alias）；标题片段匹配需要打破平局（得分最多 / 优先 TV）；标题季号与条目不符 |
| `route.go` `route` | 季号 → 沿 前传/续集 季链定位第 N 季；关联条目独有标题（目录）路由；剧场版/OVA 关键词路由到关联条目；「所有文件都被移到另一个条目 → 回到锚点」；「季号不在季链 / 季链为空 → 锚点」 |
| `route.go` `refineRouted` | 无集号正片按官方章节标题 / 播出日期对上（`matchEpisodeTitles`，已删除）；路由组按官方章节数做合理性检查（`plausible`，已删除） |
| `parse.go` | 裸数字（`Show 03`）、括号双数字取小者（`[03_51]`）、单视频取种子标题的集号、唯一无编号特别篇 → 1、同键冲突（本来就不映射） |
| `episodes_bgm.go` | 分割放送换算（文件 01 → 官方 13）：规则只接受原样在官方列表中的键；模型的答案按 `CanonicalKey` 换算后校验 |

**LLM 后端**（`internal/clef/llm.go`，默认）：
- 每个种子一次 chat completions（temperature 0，`reasoning_effort` 默认 medium（设置 `llm.reasoning_effort`），`response_format` json_schema strict；超过 60 个视频文件时分块，候选相同）。输入 JSON：种子标题、**发布时间**（released）、视频文件（index + path）、候选（id、日文 / 中文标题、最多 8 个别名（英 / 罗马字 / 中文）、无罗马字别名时日文标题假名的离线罗马字转写、类型、季、开播 / 完结日期、话数、官方正片 sort 区间如 `13–24`、特别篇如 `sp1–2, ncop1`）。输出：每个文件 `{index, subject_id（候选 id 枚举 | null）, episode（文件名里写的数字 | null）, kind, confidence, reason}` + `suggested_title`。系统提示很短（主人决定：给信息而不是规则）。
- 候选检索（`internal/meta/retrieve.go`）：所有条目名称（标题、中文名、全部别名、罗马字转写）的内存索引（30 分钟 / 目录变动后重建），统一规范化（小写、去标点空格、长音 ou/oo→o、uu→u、wo→o、去掉季 / Part 标记）；按片段完全匹配、包含、词 / 汉字二元组覆盖打分，发布时间前后在播的条目加分；取前 12 个 + 前 3 个的关联条目。召回（634 个已标注种子）：生产候选列表 100%，仅用文件名 91.5% → 97.2%。
- 有文件没选到条目且模型给出 `suggested_title` 时，用它在本地库检索（同一索引 + 前几名的关联条目），找到新候选就**再问一次**（只问这些文件）。
- 校验与 Clef 相同（候选 id、数字必须出现在文件名里、分割放送换算后在官方章节里、季数一致）。**直接应用**另需：自报置信度 ≥ 0.9（几乎所有回答都是 0.98+，不能单独用）、**标题 / 路径里出现该条目的名字**（`meta.NameEvidence`，去掉季 / Part 标记后比较）、不与文件名解析的集数 / 明确的特别篇类型矛盾、未列入官方章节的特别篇须同一种子里有同条目的已应用正片；否则进审核。
- **日期闸门**（两种后端与规则映射都有，`meta.CheckDateFull`；决策模型经 `clef.Refine`）：发布时间早于所选集的官方播出日期（分割放送换算后）7 天以上 → 人工审核，原因「日期不符」（所有种子）；只有一个视频文件的单集种子另外：晚于播出日期 30 天以上 → 审核。2 个及以上视频文件（任何类型：正片、SP、NCOP、PV、花絮）算合集，晚发布不限。该集无播出日期时用条目的播出区间（开播 − 7 天；单集种子还有完结 + 30 天）；无播出日期但前面某集晚于发布 7 天以上才播出 = 尚未播出。规则的确定映射同样经过这道闸门（不通过则写成待审核的规则决策）。**有结论的文件不再自动重新判断**（主人原则）：已映射（任何方式）、人工处理过（拒绝 / 仍然接受）、决策模型判定 `none` / `not_episode` 的文件，在种子再次出现时 `Match` 完全不动（不改写、不排审核、不建决策模型任务）；只有不确定的文件（待审核、其它原因丢弃、未判断过）会重新映射 / 交给决策模型，且只在种子再次出现时（重新抓取、重新提交、qB 元数据再次入库），没有定期重跑。「再次出现」= 新的抓取条目带着已知的 infohash（RSS / .torrent 路径，bangumi.moe 带文件的条目本来就重跑 Match）或管理员 `?scope=uncertain`：先重跑规则（`Meta.Rematch`），仍不确定的才交给决策模型；播放 / qB 元数据对已就绪的种子不算（避免每次播放都调用模型）。管理员「重新判断」是强制任务：所有文件都重新判断，可替换自动映射。发布时间相关的闸门（缺少发布时间、日期不符）只针对**新**映射：文件已存的映射与这次规则结果相同（同一条目、同一集，按分割放送换算后比较）时保持不变，不清除也不排审核（用户重新提交、抓取源重复投递、qB 元数据再次入库都会改变发布时间）；人工 / 已核实的行和决策模型的行本来就不会被改写，也不排审核；重新分类同理，且被闸门拦下的新结果只排审核、不清除原映射。原来的「条目早已完结 → 审核」（old_entry）已删除。
- **发布时间**：抓取源的 `published_at`；用户提交的种子用 `torrents.submitted_at`（迁移 11，第一次有用户提交 / 播放该种子时记录一次，同时记 `submitted_by` = 应用库的用户 id（无外键），之后不改；抓取的种子不设）。用户行的 `published_at` 不算发布时间，.torrent 的 creation date 也不用。之后被抓取到时抓取的 `published_at` 优先。**两者都没有** → 「缺少发布时间」：决策模型的结果进审核，**规则**也不自动映射——确定的规则映射写成待审核的决策记录（`clef_decisions.source = rule`），接受后写入映射（`method = review`、`status = verified`），重新分类也跳过这类种子。目的：防止有人提交伪造成正式发布名的种子来获得映射。
- 校准 v2（240 个种子，medium，候选来自不借助规则的真实检索，负样本主要是「真实条目不在库中」时的真实检索结果）：模型单独 94.3% 精度 / 98.9% 覆盖（正样本中候选里有正确条目时从未选错条目）；加上全部闸门 244/244 正确、覆盖 91.4%；中位延迟 4.4 秒、约 1.2k + 0.13k token / 种子；见 [research/decision-model-calibration.md](research/decision-model-calibration.md)。
- 并发（默认 2）、每分钟请求数、每日上限、超时都可在后台设置；API Key 为 secret。
- **重新判断异常映射**：后台按钮列出自动映射中「集数（分割放送换算后）不在官方正片列表里」的文件（只检查有官方列表的条目、整数集数），把这些种子连同这些文件（`clef_jobs.files`，迁移 10）交给决策模型；原映射保留，直到有新判断替换（`ApplyClef` 允许覆盖这些文件）。生产环境未执行。

**Clef 后端 / 共用部分**（`internal/clef`，worker 在 otd 内；映射库迁移 9：`clef_jobs`、`clef_decisions`）：
- 入队：`Match` 发现不确定的视频文件 → `clef_jobs`（每个种子一个）；worker 取最新的到期任务，只处理 3 天内入队的（停用期间的积压过期）。
- 每个种子：候选条目 ≤ 20（规则锚点与推测、标题 / 文件名 FTS 命中、前几名的 前传/续集/关联条目），选项 id `bgm_624085`，criteria = `{title, cn, aliases, air_date, eps, type, season}` + `none`；**条目请求**：`subject`（choice）+ 每个候选一个是/否问题 `v_bgm_…`（「正是这个条目吗」）；**集数请求**：每个文件 `f{i}_ep`（文件名中的数字 `n_03`、`n_1080`… + none）与 `f{i}_kind`（main/sp/op/ed/pv/cm/other）；多季合集等文件属于不同条目时改为逐文件 `f{i}_subject`。state = `{a_torrent, b_files, c_published}`。超过 64 个问题或 token 预算时拆分。
- 校验：条目必须是候选；类型 + 数字 → 集的键，经 `CanonicalKey`（分割放送换算）后必须在官方章节里；与文件名季号不符的候选概率置 0 后重新归一化（条件化）。
- 判定（阈值后台可改，校准见 [research/decision-model-calibration.md](research/decision-model-calibration.md)）：指标 = 各问题概率的最小值（条目取是/否问题的「是」概率），≥ 0.8 且概率差 ≥ 0.3 → 直接写入（`torrent_files.method = 'clef'`，之后的自动匹配不覆盖）；≥ 0.5 → 审核队列；以下不映射。条目早已完结（完结 > 1 年 / 无完结日期且开播 > 2 年）一律进审核（同名重制 / 新作可能尚未收录）。
- 每个文件一条 `clef_decisions`（选择、前几名概率、指标、模型、状态 applied/pending/rejected/discarded、原因）；后台可接受 / 拒绝 / 撤销（撤销 = 文件改为未映射且 verified）。
- 限流（每分钟请求数）、每日上限（UTC），失败指数退避（5/10/20/40 分钟，最多 5 次）；免费额度用尽（4006）与每日上限一样顺延到次日。
- 后台页 `/admin/clef`：状态、设置（「决策模型」组，按所选后端只显示相应字段）、试运行（两种后端都可）、异常映射、决策记录 / 审核队列、最近任务。
- 待改进：官方章节滞后（每周 dump）时，最新一集会被校验拒绝（discarded，不会自动重试）；独立部署的 metad 没有 worker（任务只入队）；多季合集的逐文件条目问题没有是/否确认，校准样本少。
