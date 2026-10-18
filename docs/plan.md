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
  │  - 用户/会话/角色            │──── bangumi.moe (缓存代理)
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
  规则来源（按优先级）：管理员手工判定（infohash）> 屏蔽规则（关键词/正则/文件名/bangumi.moe tag/team/分类，标记 political/porn → 只要命中一律 Block，无视优先级）> 权限规则（某作品/某 tag 仅会员/大会员，按 priority）> 默认动作（后台可设）。用户自填磁力（未受信）最终动作 = max(规则结果, 用户磁力最低权限)，只有管理员裁决或 `infohash` 字段规则能降到最低权限以下。前台理由只由 action+label 生成，不暴露规则名/裁决备注。所有拒绝写审计日志；未知/可疑的进入审核队列。实现见 `internal/policy` 包注释。
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
- **HTTP WebSeed**：实测欧洲 qB → 国内浏览器的 WebRTC 只有 ~50KB/s（高 RTT 下 SCTP 窗口受限）。利用 qB 5.3 的 `torrents/downloadFile`（支持 Range），由 otd 提供签名的 BEP19 webseed（`/webseed/{exp}.{sig}/{ih}/`），经 Cloudflare 实测 ~9MB/s。后台可设：开关、最低用户等级（默认会员，可作为大会员权益）、延迟秒数（先 P2P 再 HTTP）。只服务 qB 已完整下载的文件。
- 时间表自动选种优先 AVC/H.264（浏览器兼容），HEVC/10bit 降权。

## TODO（低优先级）
- dandanplay 弹幕（需 AppId/Secret，用户暂无账号）
- 番剧页按集分组（映射库已有文件级集数，前端目前是种子列表 + 字幕组筛选）
- 映射纠错的后台界面（API `PUT /meta/v1/torrents/{ih}/files/{idx}` 已有）
- 封面图自建缓存/缩略图（目前直链 lain.bgm.tv / bangumi.moe）
- **本集截图**：外部站点（AniList streamingEpisodes、Kitsu、TMDB still、TVDB）对当季新番的分集图要么没有、要么滞后，AniDB/bgm.tv 没有分集图。方案：qB 下载完成后，后端用 ffmpeg 经 `torrents/downloadFile`（Range）在 ~40% 处截帧（多帧取亮度正常者），存 WebP，按 (作品, 集) 存入映射库；时间表卡片优先显示本集截图。代价：镜像需加 ffmpeg。
- **时间表相邻日封面预加载**：7 天面板 DOM 已一次性渲染，但封面是原生懒加载，首次滑到某天时图片才加载。可让当天及相邻两天 eager 加载，其余在空闲时预加载（多约 1–2MB 流量）。
- **16:9 横图来源**：目前卡片为 16:9，图片用竖版封面宽度铺满、偏上裁切（object-position 50% 30%）。可选补充：TMDB backdrop（16:9，需免费 API key；bangumi-data 本季仅约 1/3 带 tmdb id，可再按标题搜索补全）；AniList bannerImage 为 1900×400（约 4.75:1，本季覆盖约 60%），裁 16:9 效果一般，不建议。
- Torznab 端点给 Sonarr
- qBittorrent#24899 观察，如复现再做 reannounce 看门狗
- 向 qBittorrent 上游反馈 #24512 过滤 wss 的副作用

## 5. 已确认
单仓库；直接用 www；注册支持邀请码/邀请链接；只有一台生产服务器；弹幕放 TODO。

## （原）待确认
1. 仓库：新建一个 monorepo（建议 `darknightlab/onlinetorrent` 新开 `v2` 分支或新仓库），还是继续前后端两个仓库？
2. 预发域名用 `next.projectk.org` 可以吗？
3. 大会员怎么来：先做“管理员授予 + 兑换码”，支付以后再说？
4. 注册：开放注册 + Turnstile，还是邀请制？是否需要邮箱验证（需要 SMTP）？
5. 生产用哪些 qB（PKU/ZJ/MCI 是否会升 5.3）？测试先只用 ams。
6. 弹幕需要 dandanplay AppId/Secret（以后再申请也行）。
