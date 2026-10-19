# onlinetorrent v2 API 契约（v1）

- Base: `/api/v1`，JSON，UTF-8。错误：`{"error":{"code":"string","message":"人类可读"}}` + 合适的 HTTP 状态码。
- 认证：登录后服务端设置 HttpOnly Cookie `ot_session`（SameSite=Lax）。写操作需带请求头 `X-Requested-With: fetch`（简易 CSRF 防护）；浏览器标记 `Sec-Fetch-Site: cross-site` 的写请求一律 403。
- 请求体：公共写接口（auth、me、play、report）最大 16KB，超出 413 `too_large`；后台接口 1MB。
- 限流（429 `rate_limited`）：按 IP 计（IPv6 按 /64）。登录另按账号计：15 分钟内失败 5 次即暂时锁定。
- 时间：RFC3339 字符串（UTC）。大小：字节 `int64`。
- 角色：`guest`（未登录）< `member` < `vip` < `admin`。
- **接口权限**（同 YotsubaNews）：每个接口注册时带权限键 `"METHOD /path/pattern"`、访问类型和默认授权。访问类型：
  `public`（所有人，不可改：site/me/登录/注册/退出）、`configurable`（游客/会员/大会员可在后台勾选）、`user`（必须登录，会员/大会员可勾选）、`admin`（仅管理员，不可改）。
  授权给某等级即自动包含更高等级；管理员通过所有接口。默认值只在接口第一次出现时写入，之后以后台设置为准。
  无权限：游客 401 `login_required`，已登录 403 `forbidden`。
- **内容范围**（同 YotsubaNews content scope）：游客 / 会员 / 大会员各有一个按标签限制可见番剧条目的范围（后台「内容范围」，见下方 `/admin/content-scopes`）；管理员不受限制。默认全部可见。范围之外的条目对该等级而言「不存在」：条目详情 / 选集 / 单集 404 `not_found`，不出现在搜索与时间表中；包含映射到它的文件的种子不出现在搜索结果中；种子收录状态与 play 的 `meta` 里不给出它的条目 / 集信息（文件按未刮削显示）；`/meta/v1` 的读取接口同样处理。

## 路径约定（同 YotsubaNews）
资源/维度/值，纯路径；只有自由文本搜索、分页等筛选才用 `?`。

**前端页面**
| 页面 | 路径 |
|---|---|
| 首页（新番时间表） | `/` |
| 播放（自动选视频文件） | `/watch/hash/{infohash}` |
| 播放指定文件 | `/watch/hash/{infohash}/file/{index}` |
| 条目（单季） | `/subject/{provider}/{id}`：`id`（本站数字 ID）、`bgm`、`mal`、`anidb`、`anilist`、`tmdb`… |
| 单集（自动选资源播放） | `/subject/{provider}/{id}/episode/{key}`（`key` 见下方「集的键」；指定资源 `…/episode/{key}/hash/{ih}[/file/{index}]`） |
| 搜索 | `/search/{keyword}`（翻页 `?page=`） |
| 登录 / 注册 / 邀请注册 | `/login`（`?next=`）/ `/register` / `/register/invite/{code}` |
| 个人中心 / 后台 | `/me` / `/admin/...` |
非规范写法的集的键（`SP01`、`ncop`、`012`）前端重定向到规范写法；无效键显示 404。
旧链接兼容：`/?uri=磁力`、`/watch?uri=|ih=|t=`、`/anime/{key}` 均重定向到新路径。

**API**
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/schedule` | 新番时间表 |
| GET | `/api/v1/subjects/{provider}/{id}` | 条目详情 + 本地种子（映射到该条目的） |
| GET | `/api/v1/subjects/{provider}/{id}/episodes` | 选集列表：正片 + 特别篇（SP / NCOP / NCED / PV / CM） |
| GET | `/api/v1/subjects/{provider}/{id}/episodes/{n}` | 单集所有已知文件（含当前用户 access 与做种统计 swarm），由前端按推荐规则排序；`{n}` 为集的键 |
| GET | `/api/v1/search/{keyword}` | 搜索：番剧 + 种子（本地库；翻页 `?page=`） |
| GET | `/api/v1/torrents/hash/{ih}` | 种子收录状态与文件刮削结果（做种后轮询） |
| POST | `/api/v1/torrents/hash/{ih}/play` | 打开种子（策略判定 + 磁力） |
| POST | `/api/v1/torrents/moe/{id}/play` | 按 bangumi.moe ID 打开（本地库中由 bangumi.moe 抓取源收录的种子） |
| POST | `/api/v1/torrents/hash/{ih}/seed` / `report` | 服务器做种 / 举报 |
| GET | `/api/v1/torrents/hash/{ih}/torrent` | 种子文件（.torrent，HTTP 元数据；磁力 `xs=` 指向这里） |
| GET | `/api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]` | WebSeed 就绪推送（SSE） |
| GET | `/webseed/hash/{ih}/[文件路径]` | HTTP WebSeed 文件（Cookie 鉴权） |
| GET | `/meta/v1/torrents/hash/{ih}` | 文件级映射 |
| PUT | `/meta/v1/torrents/hash/{ih}/files/{idx}` | 纠正映射（admin），body `{subject_id|work_key, episode_key | (episode, kind), status}` |
| GET | `/meta/v1/subjects/{provider}/{id}` / `.../episodes` / `.../episodes/{key}/files` / `/meta/v1/subjects/search?q=` | 映射库条目 / 有文件的集 / 单集文件 |
| * | `/api/v1/admin/{resource}/id/{id}` | 后台单项资源 |
| GET/POST | `/api/v1/admin/sources`，`/admin/sources/schema`，`/admin/sources/probe` | 抓取源列表 / 新建 / 类型说明 / 测试 |
| PUT/DELETE | `/api/v1/admin/sources/id/{id}`，POST `…/id/{id}/run` | 抓取源修改 / 删除 / 立即抓取 |

> 已移除：`GET /api/v1/torrents/search?q=`（改为 `GET /api/v1/search/{keyword}`）。用户请求路径上不再实时请求 bangumi.moe：搜索、条目种子列表、时间表最新一集全部读本地库（由后台抓取源填充，见下方「抓取源」）。

## 集的键（episode key）
URL 与 API 中用来指代某一集（Go：`meta.ParseEpisodeKey`，`internal/meta/episode.go`；TS：`parseEpisodeKey`，`web/src/lib/episode.ts`）。
| 类型 `kind` | 键 | 显示名 `label` / 按钮 | 映射库 `torrent_files.kind` |
|---|---|---|---|
| `main` 正片 | 数字原样：`12`、`12.5`（总集篇）、`0` | `第 12 话` / `12` | `episode` |
| `sp` 特别篇（SP / OVA / OAD / Special） | `sp1` | `SP1` | `sp` |
| `ncop` 无字幕片头 | `ncop1` | `NCOP1` | `op` |
| `nced` 无字幕片尾 | `nced1` | `NCED1` | `ed` |
| `pv` 宣传片（PV / Trailer / Teaser） | `pv1` | `PV1` | `pv` |
| `cm` 广告 | `cm1` | `CM1` | `cm` |
- 编号 ≥ 0，可为小数（最多 3 位）；正片 ≤ 5000，特别篇 ≤ 999。没有编号的特别篇记为 1（`ncop` 是 `ncop1` 的别名）。
- 解析不区分大小写、忽略前导零（`SP01` → `sp1`，`012` → `12`）；接口返回的 `key` 一律为规范写法（小写、无前导零）。其他写法（`op1`、`-1`、`12.`）400 / 页面 404。
- 排序：正片按编号，然后 sp、ncop、nced、pv、cm 各自按编号。

## 公共

### `GET /site`
```json
{ "name": "Project Kawaii", "announcement_md": "...markdown...",
  "registration": "open|invite|closed", "turnstile_site_key": "",
  "trackers": ["wss://tracker.projectk.org", "..."],
  "ice_servers": [{"urls": ["stun:stun.l.google.com:19302"]}],
  "max_auto_size_bytes": 2147483648,
  "rank_rules": { ...推荐规则，见下方「推荐规则」... } }
```

### `GET /me` → `{"user": null, "permissions": [...]}` 或
```json
{ "user": { "id": 1, "username": "a", "email": "a@x", "role": "member", "effective_role": "member", "vip_until": null, "created_at": "..." },
  "permissions": ["GET /api/v1/schedule", "POST /api/v1/torrents/hash/{ih}/play", "POST /api/v1/torrents/hash/{ih}/seed", "..."] }
```
`permissions` 为当前身份可调用的非公开、非后台接口权限键，前端据此决定是否请求服务器做种 / WebSeed、是否显示举报等。

### `POST /auth/register` `{username, email, password, invite_code?, turnstile_token?}` → `/me` 结构（并登录）
### `POST /auth/login` `{identifier, password}` → `/me` 结构
### `POST /auth/logout` → `{}`
### `POST /me/password` `{old_password, new_password}` → `{}`
### `POST /me/redeem` `{code}` → `/me` 结构（兑换码 → vip；不区分大小写。永久大会员 / 管理员兑换返回 400「你已是永久大会员」，兑换码不会被消耗）

### `GET /schedule`
番剧时间线（bangumi-data 本季 + 上季仍在播）。`items` 按 `next_air_at`/`last_air_at` 计算，前端按星期分组。
```json
{ "generated_at": "...", "season": "2026-10",
  "items": [{
    "key": "bgm:123456",          // 映射库内部键
    "subject_id": 1034,            // 本站条目 ID → /subject/id/1034
    "title": "葬送のフリーレン",   // 原名
    "title_cn": "葬送的芙莉莲",    // 可能为空
    "cover": "https://.../cover.jpg", // 可能为空，前端要有占位
    "begin": "2026-10-03T14:00:00Z",
    "weekday": 6,                  // 0=周日 … 6=周六，按 Asia/Shanghai 计算
    "air_time": "22:00",           // Asia/Shanghai 时间
    "last_air_at": "2026-10-10T14:00:00Z",
    "next_air_at": "2026-10-17T14:00:00Z",
    "episodes_aired": 2,           // 估算
    "bgm_id": 123456, "moe_tag_id": "5ff…",   // bgm_id 可为 null
    "latest_episode": 2             // 映射库已知的最新一集（正片，可为 null）→ 前端卡片直达 /subject/…/episode/{latest_episode}
  }]
}
```

### `GET /subjects/{provider}/{id}`
```json
{ "subject": { ...同 schedule item..., "summary": "简介", "type": "tv", "official_site": "https://…",
               "tags": ["原创", "..."], "meta_tags": ["TV", "原创"], "nsfw": false, "score": 8.3, "rank": 85, "eps": 25,   // 来自 bangumi Archive；score/rank/eps 为 0 时省略
               "external": {"bgm": "123", "mal": "1", "bangumi_moe": "5ff…"} },   // external 的值目前为字符串
  "torrents": [Torrent, ...] }     // 本地库中至少有一个文件映射到该条目的种子，按发布时间倒序，最多 100 个
```

### `GET /subjects/{provider}/{id}/episodes`
选集列表。接口权限可配置，默认游客可用。
```json
{ "items": [
  { "key": "1",     "kind": "main", "number": 1,    "label": "第 1 话",    "sources": 0, "aired": true,
    "official": true, "title": "魔王的苏醒之日", "title_ja": "魔王 が 目覚める 日", "airdate": "2008-04-06" },
  { "key": "12.5",  "kind": "main", "number": 12.5, "label": "第 12.5 话", "sources": 2, "aired": true, "official": false },
  { "key": "sp1",   "kind": "sp",   "number": 1,    "label": "SP1",        "sources": 1, "official": true, "title": "特別編" },
  { "key": "ncop1", "kind": "ncop", "number": 1,    "label": "NCOP1",      "sources": 3, "official": false } ] }
```
- 内容：bangumi 官方章节（正片 / SP / OP→ncop / ED→nced / PV；不含 MAD 与「其他」）∪ 所有有映射文件的集。没有官方正片列表时，正片用 `1..max(episodes_aired, 已映射的最大整数正片)`（最多 2000）代替。排序见「集的键」。
- `sources`：映射到该集的文件数（不含 rejected）；0 = 暂无资源。`aired`（仅正片）：有官方播出日期时按日期（JST）判断，否则按时间表估算；有资源的集一律视为已播。
- `title`：官方标题（有中文名时为中文，`title_ja` 为日文原名；没有中文名时 `title` 为原名）。`official`：该集在 bangumi 章节列表中。
- 分割放送：bangumi 第二季正片若从 13 开始，而文件写 01，则读取时自动换算为 13（文件里存的仍是原始集数）。

### `GET /subjects/{provider}/{id}/episodes/{n}`
`{n}` 为集的键（`12`、`12.5`、`0`、`sp1`、`ncop1`…；权限键仍是 `GET /api/v1/subjects/{provider}/{id}/episodes/{n}`）。无效键 400 `bad_request`。
```json
{ "subject": { ...同上... }, "key": "ncop1", "kind": "ncop", "number": 1, "label": "NCOP1",
  "episode": 3,                       // 仅正片（kind=main）给出，等于 number

  "items": [{ "infohash": "...", "file_index": 0, "path": "[Team] Foo - 03 [1080p].mkv", "size_bytes": 0,
              "torrent_title": "...", "team": "字幕组名", "published_at": "...", "source_id": "来源条目 ID（bangumi.moe _id / RSS guid），可空",
              "source": "抓取源名称（bangumi.moe、Nyaa…）或 user",
              "confidence": 0.85, "access": {"action": "allow", "reason": ""},
              "swarm": Swarm | null }] }
```
`items` 按发布时间倒序（中性顺序），**不再由服务端推荐**：前端按 `/site.rank_rules` 与当前用户权限打分排序（`web/src/lib/rank.ts`，见「推荐规则」），列表第一项自动播放。无已知文件时 `items` 为空数组。`swarm` 见「做种统计」。特别篇的文件来自 `torrent_files` 中对应的 kind（见「集的键」）。

### `GET /search/{keyword}?page=1`
关键词放在路径里（需 URL 编码，最长 100 字）；`page` 上限 50。每 IP 突发 20 次、之后每 3 秒 1 次。全部读本地库（SQLite FTS5 trigram，中日文可搜；关键词中任一词少于 3 个字时改用 LIKE 子串匹配）。
- `subjects`：只在第 1 页返回（其余页为 `[]`），最多 24 个。本地作品按原名 / 中文名 / 别名匹配；**本地没有任何匹配时**按需查询 bgm.tv 搜索 API（动画）以及 TMDB（后台设置了 `upstream.tmdb_api_key` 时，动画类剧集 / 电影），结果写入映射库成为本地作品（获得稳定的 `subject_id`）后返回。无结果的关键词缓存 1 小时；外部请求全局限速（每 IP 每 30 秒最多触发 5 次）。外部查询失败时 `subjects` 为空，不报错。
- `torrents`：本地种子的标题 + 种子内文件名匹配，按发布时间倒序，每页 30 个；带当前用户的 `access`。
- 磁力 / infohash 不走搜索：前端识别后直接进入 `/watch/hash/{ih}`。
```json
{ "subjects": [Subject, ...],     // 同 GET /subjects/{provider}/{id} 的 subject（不含按需拉取的简介；summary 可能为空）
  "torrents": [Torrent, ...],
  "page": 1, "page_count": 3,      // page_count ≥ 1，最多 50
  "total": 75 }                    // 匹配的种子总数
```
Torrent：
```json
{ "id": "bangumi.moe 抓取源收录的为 bangumi.moe _id，其余为 ih:{infohash}",
  "infohash": "40位小写hex",
  "title": "[字幕组] xxx - 02 [1080p]",
  "size_bytes": 367001600, "files": 1,
  "team": {"id": "...", "name": "..."} ,      // 可空；RSS 来源 id 为空、name 取标题开头的 [组名]
  "tags": [{"id": "...", "name": "..."}],      // bangumi.moe 标签；其他来源为 []
  "published_at": "...",
  "source": "抓取源名称（bangumi.moe / 蜜柑计划 / Nyaa / 动漫花园…，后台可改名）或 user",
  "seeders": 0, "leechers": 0,                 // = swarm.bt_seeders / bt_leechers（未知为 0，保留字段）
  "swarm": Swarm | null,                       // 见「做种统计」
  "access": { "action": "allow|login|vip|block", "reason": "" }
}
```
受信：后台把抓取源标记为「受信」（默认只有 bangumi.moe）后，其种子与原来的 bangumi.moe 种子一样不受「用户自填磁力最低权限」限制；未受信来源（如 Nyaa、蜜柑）的种子与用户磁力同等对待（默认需登录）。

### `GET /torrents/hash/{ih}`
种子在本地库中的收录状态与逐文件刮削结果，供播放页在「服务器做种」后轮询（建议 3–5 秒一次）。只读本地数据；唯一的外部请求是做种统计过期时的按需 scrape（见「做种统计」，最多等待 1.5 秒）。接口权限可配置，默认游客可用。
```json
{ "status": "unknown|pending|ready",
  "title": "种子标题（本地库 / qB 元数据名称；未知时为空串）",
  "files": [{ "index": 0, "path": "目录/文件名.mkv", "size_bytes": 0,
              "kind": "episode|sp|op|ed|pv|cm|extra|other",
              "subject": { "subject_id": 1034, "bgm_id": 123456, "title": "原名", "title_cn": "中文名" },   // 可省略：未刮削到条目；bgm_id 可省略
              "episode": 2,                // 可省略：仅正片（kind=episode）且有 subject 时给出；可为小数（12.5）
              "episode_key": "2" }],       // 可省略：有 subject 时给出集的键（"12.5"、"sp1"、"ncop1"…）
  "swarm": Swarm | null }
```
- `ready`：文件列表已知（`files` 为全部文件，包含字幕、字体等 `other`）。`pending`：正在获取元数据（抓取源的磁力在 qB 元数据队列中、刚请求过服务器做种 30 分钟内、或做种后的检查进行中），`files` 为 `[]`。`unknown`：本地没有该种子，或获取失败（`title` 可能仍有值）。
- 前端：若当前播放文件刮削到 `subject` + `episode_key`（任意类型），切到 `/subject/{bgm|id}/{…}/episode/{episode_key}/hash/{ih}/file/{index}`。

### `POST /torrents/hash/{ih}/play` / `POST /torrents/moe/{id}/play`（无 body）
服务端：解析 → 策略判定 → 返回带 wss tracker 的磁力（**不再推送给 qB**，见下方 seed）。默认游客可用。本地库已收录的种子（抓取源）使用库中的标题、文件列表与受信标记；`moe/{id}` 只查本地库（bangumi.moe 抓取源收录过的），找不到返回 404。
- 200：
```json
{ "access": {"action":"allow","reason":""},
  "infohash": "...", "name": "...", "magnet": "magnet:?xt=...&tr=wss://...",
  "seeded_by": [], "webseeds": [] ,           // 恒为空，保留字段兼容
  "meta": { "subject_id": 1034, "subject_key": "bgm:123", "bgm_id": "123", "title": "…",
            "episode": 2,                      // 仅正片，特别篇为 null
            "episode_key": "2", "episode_label": "第 2 话",   // 特别篇如 "ncop1" / "NCOP1"
            "file_index": 0 } }   // meta 可空：种子中第一个映射到条目的正片文件，没有正片时取第一个特别篇文件
```
- 401 `{"error":{"code":"login_required"}}`，402 `{"error":{"code":"vip_required"}}`，451 `{"error":{"code":"blocked","message":"原因"}}`。
- `magnet` 末尾可能带 `&xs=<站点地址>/api/v1/torrents/hash/{ih}/torrent`（URL 编码；站点地址同 WebSeed：设置 `site.base_url`，为空时 `https://{Host}`）：当前用户有 `GET /api/v1/torrents/hash/{ih}/torrent` 权限且元数据可得时才加——本地已存 .torrent、bangumi.moe 抓取源收录的种子，或某台在线 qB 已有该种子的元数据（`torrents/info`，1.5 秒超时；结果缓存：有 5 分钟、无 15 秒）。WebTorrent 启动时会同源请求 xs 地址（带登录 Cookie），校验 infohash 后直接作为元数据，不必等 WebRTC 节点通过 ut_metadata 提供（校园网等无法建立 WebRTC 连接时也能显示文件列表、使用 WebSeed）。
- 用户磁力里的 `tr=` 一律丢弃（只用站点配置的 tracker）；`dn=` 只作为未受信标题参与判定（用户磁力最低权限始终生效），不写入种子标题，真实标题以 qB 元数据为准。
- 用户提交的种子获取元数据后会被复查；此外后台每 10 分钟用 qB 中的真实名称与文件列表复查本站分类下所有种子，命中屏蔽规则或超过大小上限的会被移除。

### `POST /torrents/hash/{ih}/seed` → `{"seeded_by": ["ams"]}`
让所有在线 qB 下载并做种（消耗服务器带宽 / 磁盘）。推给 qB 的 tracker 由本站管理（不依赖 qB 的「自动添加 tracker」）：`webtorrent.trackers` + `bt.trackers` + 种子自带的 announce 列表（本地存的 .torrent），去重后最多 30 个；磁力带上全部，`torrents/add` 之后再 `torrents/addTrackers` 一次（qB 跳过已有的，所以已在 qB 里的种子也会补上缺少的 tracker）。wss 只有在该 qB 开启 `add_wss_trackers` 时才经 addTrackers 添加（qB 会过滤磁力里的 wss）。抓取源的 qB 元数据队列同样使用 Feed 磁力的 tracker + 以上列表。默认仅会员及以上。种子须先经 play 打开过；会对当前用户重新做策略判定。超过 `play.max_push_size_mb` 返回 413 `too_large`。
qB 拿到元数据后，后端把文件列表写入本地库并刮削（每个 infohash 一次）；期间 `GET /torrents/hash/{ih}` 返回 `pending`，完成后 `ready`。若该种子正好在抓取源的元数据队列里（qB 分类 `projectk-meta`），会被转入做种分类并开始下载，不会被队列删除。

### `GET /torrents/hash/{ih}/torrent` → `application/x-bittorrent`
种子文件（v1 infohash 与 `{ih}` 一致的 bencode 原文，≤ 5 MB），用作浏览器的 HTTP 元数据来源（见 play 的 `xs=`）。接口权限可配置，默认游客可用；另按当前用户对该种子的策略判定（与 WebSeed 相同，按 角色+种子 缓存 1 分钟），不允许时 403 `forbidden`（本地没有记录的种子按用户磁力处理，默认需登录）。每 IP 突发 30 次、之后每 2 秒 1 次。`Cache-Control: private, max-age=3600`。
- 来源（依次）：映射库表 `torrent_metainfo`（迁移 7）→ 在线 qB `torrents/export`（qB ≥ 4.5）→ bangumi.moe 抓取源收录的种子按需下载 `{bangumi.moe 地址}/download/torrent/{_id}/{ih}.torrent`（与抓取共用同站串行 + 间隔，失败缓存 10 分钟）；后两者成功后写入表中。都没有时 404 `not_found`；infohash 无效 400。
- 写入时机：抓取源下载 .torrent 时；qB 元数据队列取到元数据后（删除前导出）；会员做种后的检查 / 每 10 分钟复查拿到元数据时。表中的种子文件不随 qB 删除。
- 前端兜底：磁力没有 `xs=`（例如刚粘贴的未知磁力）且有做种权限时，播放页在会话仍无元数据时每 3 秒轮询 `GET /torrents/hash/{ih}`（最多 3 分钟），`ready` 后下载本接口的种子文件，移除会话中尚无元数据的种子并用种子文件重新添加（日志「已从服务器获取种子信息」）。

### HTTP WebSeed（固定地址，Cookie 鉴权）
- 文件：`GET /webseed/hash/{ih}/`（单文件种子）或 `/webseed/hash/{ih}/{种子内文件路径}`（多文件，webtorrent 自动拼接）。
  不带签名/过期时间；每次请求用登录 Cookie 校验接口权限 `GET /webseed/hash/{ih}/{path...}`（后台「接口权限」可改，默认会员）+ 当前用户对该种子的策略。只提供 qB 已完整下载的文件，否则 404。`Cache-Control: private, no-store`。
- 就绪推送（SSE）：`GET /api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]`（种子须先经 play 打开）。事件：`waiting {progress}`（进度变化时）、`ready {webseeds:[固定地址]}`、`unavailable {reason: disabled|not_seeded}`、`timeout {}`（30 分钟）；每 25 秒一条 `: ping` 心跳；每 IP 最多 6 条并发连接。
- 前端：`/site.webseed = {enabled}`；播放开始即订阅推送（SSE 自己等待元数据与服务器文件就绪），收到 `ready` 才 `addWebSeed`，不做客户端探测。

### `POST /torrents/hash/{ih}/report` `{reason}` → `{}`  需登录。举报 → 进入审核队列（标题取自服务端记录，不接受客户端提供；reason 最长 500 字）


## 做种统计（swarm）
`Swarm`（单集 items、条目 / 搜索的 Torrent、`GET /torrents/hash/{ih}` 的 `swarm` 字段；从未统计且不在服务器上时为 `null`）：
```json
{ "bt_seeders": 23, "bt_leechers": 8,     // UDP / HTTP tracker；null = 该类 tracker 都没有响应过
  "wt_seeders": 1, "wt_leechers": 0,      // WebTorrent（wss）tracker；null 同上
  "server_complete": true,                // 某台在线 qB 已完整下载（qB 种子列表快照，缓存 1 分钟）
  "checked_at": "2026-10-19T04:04:01Z" }  // 计数的统计时间；null = 尚未统计（只有 server_complete 可能已知）
```
- **按需、缓存 30 分钟，不做全库定时查询**：上述接口返回的种子中，没有数据或 `scraped_at` 超过 30 分钟的，当场只查询这些种子——按 tracker 分组批量请求（UDP BEP 15 每包 ≤ 74 个 infohash、connection id 复用约 1 分钟、每次 1.5/2.5/4 秒重试；HTTP scrape 一次多个 `info_hash`（announce → scrape）、5xx 重试一次；wss 发 `{"action":"scrape","info_hash":[latin1 字符串…]}`，每条 ≤ 50 个），所有 tracker 并行；响应最多等待 **1.5 秒**（约 1.2 秒时先把已响应 tracker 的结果写库），返回当时已知的数据（新值、旧值或 null），其余在后台完成后写库。同一 infohash 的并发请求只查询一次（in-flight 集合）；每个 tracker 限速（突发 4 次，之后每秒 1 次）；同时最多 8 组按需查询，再多时直接返回库中数据。每次请求最多刷新 100 个种子。
- tracker：`bt.trackers` ∪ `webtorrent.trackers` ∪ 种子自带 announce 列表中前 3 个 udp/http（不在配置里的；每次最多 16 个这类 tracker；必须解析到公网地址）。计数取各 tracker 的**最大值**（同一节点会向多个 tracker 汇报，不能相加）：BT = UDP/HTTP tracker 的最大值，WebRTC = wss tracker 的最大值。响应了但没有列出某个种子的 tracker 记为 0。
- Nyaa RSS 的 `nyaa:seeders` / `nyaa:leechers` / `nyaa:downloads` 在抓取时写为初始值（`checked_at` = 抓取时间），第一次真正 scrape 前有效，之后不再覆盖。
- 设置 `swarm.enabled`（默认开）：关闭后不再查询，只返回已有数据。
- 存储：映射库迁移 8：`swarm_stats(infohash PK, bt_seeders, bt_leechers, bt_completed, wt_seeders, wt_leechers, server_complete, server_progress, source scrape|rss, checked_at, scraped_at, error)`；`torrent_metainfo.announce`（JSON，种子文件的 announce-list，旧行首次使用时补齐）。
- tracker 状态是**被动**的：只记录每个已配置 tracker 最后一次实际使用（上述按需查询）时的结果，从不为检测主动发请求；保存在内存（重启后为「尚未使用」）。

## 推荐规则
`/site.rank_rules`（后台「推荐规则」编辑，设置键 `rank.rules`，不在 `/admin/settings` 中；`/site` 返回规范化后的完整对象）。单集页在浏览器里给每个文件打分（`web/src/lib/rank.ts`，纯函数），按「当前用户可播放（策略 allow）→ 总分 → 发布时间新 → 原顺序」排序，自动播放第一个；策略不允许的始终排最后、不会被自动播放。自动选中后在本页固定（做种人数刷新导致重新排序时不切换）。默认值的依据与 39 个场景见 [research/rank-rules.md](research/rank-rules.md)。
```json
{ "availability": { "server_complete": 2,            // 服务器已缓存（小加分）
                    "wt_seeder": 2, "wt_cap": 6,     // WebTorrent 做种：系数、上限
                    "bt_seeder": 1, "bt_cap": 4,     // BT 做种（只对有做种权限 POST …/seed 的用户）
                    "curve": "log",                  // log：系数 × log2(1+人数)；linear：每人 系数 分（缺省 = linear）
                    "dead": -100 },                  // 无人做种，见下
  "keywords": [ {"label": "简体中文", "pattern": "简|chs|\\bsc\\b|\\bgb\\b|zh-?hans", "score": 25, "group": "字幕"},  // 不区分大小写，匹配 种子标题 + 文件路径
                {"label": "繁体中文", "pattern": "繁|cht|\\btc\\b|big5|zh-?hant", "score": 12, "group": "字幕"},       // 同一 group 只取命中的最高分
                {"label": "1080p", "pattern": "1080[pi]?\\b|1920x1080", "score": 10, "group": "分辨率"},
                {"label": "4K", "pattern": "2160p?\\b|3840x2160|\\b4k\\b", "score": 5, "group": "分辨率"},
                {"label": "720p", "pattern": "720p\\b|1280x720", "score": 4, "group": "分辨率"},
                {"label": "HEVC", "pattern": "hevc|[hx]\\.?265", "score": 2, "group": ""} ],
  "teams": [ {"name": "字幕组名（精确匹配）", "score": 5} ],   // 默认 []
  "size": { "max": 6, "full_below_mb": 300, "zero_above_mb": 1500 },  // 小文件加分：≤300 MB 满分，≥1500 MB 0，中间线性；大小未知不加
  "oversize": -10 }   // 文件大于 play.max_auto_size_mb
```
- 分数可为小数（显示取整，tooltip 给明细）。
- 无人做种（`dead`）：没有做种或 WebSeed 权限的用户（游客类）：`wt_seeders == 0` 且服务器未缓存；有做种或 WebSeed 权限的用户：所有来源都为 0（BT 0、WebRTC 0、服务器未缓存）。从未统计（`checked_at` 为 null）或对应 tracker 都没响应（计数 null）时可用性记 0、不扣分。
- 校验（PUT 400 `bad_request`）：未知字段报错；分值 / 系数 −1000…1000，上限 0…1000；`curve` 为 `linear|log`；`size` 的两个大小 0…1048576 MB，`max ≠ 0` 时 `zero_above_mb > full_below_mb`；关键词 ≤ 30 个（名称 ≤ 40 字、正则 ≤ 200 字、分组 ≤ 20 字），字幕组 ≤ 200 个（名称 ≤ 80 字、不重复）；正则须为 Go RE2 与 JavaScript（`i` 标志、无 `u`）共同支持的子集：能被 RE2 编译（因此没有前后查找、反向引用），且不能用 `(?…)`（`(?:` 除外）、`\A \z \Q \E \C`、`\p{…}`、`\x{…}`、`[[:alpha:]]`。
- 升级：库中的 `rank.rules` 若仍等于旧版内置默认值（JSON 相等，即管理员没改过），启动时替换为当前默认值；改过的保留。

## 后台 `/admin/*`（仅 admin）
- `GET /admin/permissions` → `{"roles":["guest","member","vip"], "items":[{key, method, path, group, label, access, default_roles, granted:{guest,member,vip}}]}`
- `PUT /admin/permissions` `{"grants": {"POST /api/v1/torrents/hash/{ih}/seed": ["member"], ...}}` → 同 GET（只能改 configurable / user 类型；user 类型不能授权给 guest）
- `GET/PUT /admin/settings` 全部站点设置（KV 对象，字段见 `GET` 返回）
- `GET/POST /admin/qb-servers`，`PUT/DELETE /admin/qb-servers/id/{id}`，`POST /admin/qb-servers/id/{id}/test`
  QBServer: `{id,name,url,username,password(写入时),api_key(写入时),category,enabled,seeding_time_limit_min,ratio_limit,max_active,online,version,last_error,last_check_at}`
- `GET /admin/users?q=&page=`，`PUT /admin/users/id/{id}` `{role?, vip_until?, disabled?}`（设 `role: "vip"` 且不传 `vip_until` = 永久大会员，会清空 vip_until）
- `GET/POST /admin/invites`、`GET/POST /admin/redeem-codes`
  创建邀请 `{max_uses?, role, vip_days, expires_in_days, note}`：`max_uses` 省略为 1，0 为不限，负数 400；`vip_days` 需 ≥ 0。邀请码大写，使用时不区分大小写。
- `GET/POST /admin/rules`，`PUT/DELETE /admin/rules/id/{id}`
  Rule: `{id, name, enabled, priority, match: {"field":"title|file|infohash|team|tag|category","op":"contains|regex|equals","value":"..."}, action:"allow|login|vip|block", label:"political|porn|copyright|other|", note}`
- `POST /admin/policy/test` `{title, infohash?, files?, tags?, team?}` → `{access, matched_rule}`
- `GET /admin/reviews?status=pending` / `PUT /admin/reviews/id/{id}` `{action, label, note}`（写入 infohash 级裁决；action 为 block 或 label 为 political/porn 时同时从 qB 移除。note 仅后台可见，前台理由由 action+label 生成）
- 日志保留：plays / audit 保留 90 天，已处理的 reviews 保留 180 天（每日清理）。
- 内容范围（仅 admin；写入记审计日志 `content_scope.update`，保存后立即生效——编译结果缓存在内存，写入时替换）：
  - `GET /admin/content-scopes` → `{"roles":["guest","member","vip"],"items":[ContentScope]}`
  - `PUT /admin/content-scopes/role/{role}` body = ContentScope（`role` 可省略）→ 规范化后的 ContentScope。`{role}` 为 `admin` 时 400（管理员不受限制），未知等级 404。校验（400 `bad_request`）：`tag_mode` 为 `all|selected`；`tags`、`exclude_tags` 各最多 50 个，每个去掉首尾空白后非空且 ≤ 60 字；`tag_operators` 长度必须等于 `len(tags) - 1`，每项为 `and|or`；`selected` 模式至少一个标签；出现未知字段也 400。`all` 模式会清空 `tags` / `tag_operators`；`exclude_tags` 去重排序。
  - `GET /admin/subject-tags?q=` → `{"items":[{"name":"原创","count":6867}]}`：标签自动补全，来自 works.tags ∪ meta_tags（`count` = 带该标签的条目数），包含 `q` 的标签，以 `q` 开头的排前，再按条目数，最多 30 个；`q` 为空时给出最常见的标签。
  ```json
  ContentScope: { "role": "guest", "tag_mode": "all|selected",
                  "tags": ["原创", "校园", "战斗"], "tag_operators": ["and", "or"],   // 原创 AND 校园 OR 战斗
                  "exclude_tags": ["18禁", "R18", "里番"] }
  ```
  - 条目的标签 = `works.tags`（bgm 用户标签）∪ `works.meta_tags`（bgm 公共标签），按原样精确匹配。范围只看标签，不使用 `works.nsfw` 字段；要隐藏成人内容请排除 `里番` / `R18` / `18禁` 等标签。
  - 表达式：AND 优先于 OR——在每个 OR 处分组，条目拥有某一组的全部标签即匹配：`原创 AND 校园 OR 战斗` = `(原创 AND 校园) OR 战斗`。`selected` 模式只显示匹配的条目；`exclude_tags` 在两种模式下都生效（带有任意一个即隐藏，优先于表达式）。
  - 注意：没有任何标签的条目（如只来自 bangumi-data、尚未被 bangumi Archive 补全的条目，或按需从 bgm.tv / TMDB 搜索写入的条目）在 `selected` 模式下被隐藏。
  - 搜索：本地条目按范围在 SQL 中过滤（不影响每页数量）；只有本地完全没有匹配（不论范围）时才查询外部，外部结果同样按范围过滤。
- Tracker（后台页 `/admin/trackers`）：`GET /admin/trackers` →
  `{"items":[{url, kind:"udp|http|wss", list:"bt|webtorrent", status:"ok|error|unused", result:"ok|timeout|dns|network|protocol|error|", latency_ms, last_error, checked_at, last_ok_at, last_error_at, ok_count, fail_count}],
    "swarm":{enabled, running, runs, torrents, last_at, last_ms, last_torrents, last_answered, skipped_busy},
    "stats":{torrents, scraped, known, dead}, "lists":{"webtorrent":[…],"bt":[…]}}`。
  状态为最后一次实际使用时的结果（被动记录，启动以来；未使用为 `unused`），计数为启动以来。两个列表在该页编辑，经 `PUT /admin/settings` 保存（`webtorrent.trackers` 只接受 ws/wss，`bt.trackers` 只接受 udp（需端口）/ http(s)，各 ≤ 50 个，去空白去重）。
- 推荐规则（后台页 `/admin/rank-rules`）：`GET /admin/rank-rules` → `{rules, defaults}`；`PUT /admin/rank-rules` body = 规则对象 → `{rules（规范化后）, defaults}`，记审计日志 `rank_rules.update`。
- 决策模型（Cloudflare Workers AI · Clef，后台页 `/admin/clef`；设计见 plan.md「规则收紧 + 决策模型 Clef」，阈值校准见 research/clef-calibration.md）：
  - `GET /admin/clef` → `{enabled, configured, model, base_url, thresholds:{metric,apply,margin,review}, rate_per_min, daily_cap, today, jobs:{pending,done,failed,expired}, decisions:{applied,pending,rejected,discarded}, recent_jobs:[ClefJob], current?, last_error?, last_error_at?, last_ok_at?, session:{requests,input_tokens}}`。`today` = 今天（UTC）已发请求数（含试运行）。ClefJob：`{infohash, title, status: pending|done|failed|expired, attempts, requests, next_at, last_error, created_at, updated_at}`。
  - `POST /admin/clef/test` `{infohash}` → `{rules:[{index,path,kind,work_key?,episode?,queue,hint?}], outcome:{task:{title,published,files,candidates,per_file}, model, requests, answers, decisions:[{file, candidate?, kind, episode?, episode_key?, score, margin, confidence, answers, status, reason?}], usage, sent}, error?}`：试运行（不写库，计入每日上限；结果里的 `status` = 会发生什么），对本地库中的种子全部视频（extra 除外）请求一次；`rules` 是当前规则的结果。infohash 无效 400；不在本地库 404；未配置 Account ID / Token 400 `not_configured`；调用失败时 200 并带 `error`。
  - `GET /admin/clef/decisions?status=applied|pending|rejected|discarded&ih=&page=` → 分页 `{items:[ClefDecision], page, page_count, total}`（每页 30，新的在前）。ClefDecision：`{id, infohash, file_index, path, torrent_title, work_key?, subject_id?, title?, title_cn?, kind, episode, episode_key?, score, margin, confidence, answers:{问题 id: {choice, confidence, top:[{id,p}], conditioned?}}, model, status, reason, created_at, decided_at?, decided_by?}`。`reason`：`none`（模型选「无」）、`invalid_subject`、`not_episode`、`no_number`、`invalid_number`、`episode_not_official`、`season_mismatch`、`low_confidence`、`old_entry`（pending：条目早已完结，不自动应用）、`conflict`、`no_answer`。`answers.verify` = 各候选「正是该条目」的「是」概率。
  - `POST /admin/clef/decisions/id/{id}/accept`：pending → 重新校验并写入映射（`method = clef`），状态 applied；`POST …/reject`：pending → rejected；applied → **撤销**（文件改为未映射、`status = verified`、`method = manual`，之后自动匹配不再动它），状态 rejected。状态不允许或文件已被改动 409 `conflict`。均记审计日志（`clef.accept` / `clef.reject`）。
  - `POST /admin/clef/jobs/hash/{ih}/run` → 202 + `GET /admin/clef` 结构：把种子（重新）加入队列并唤醒 worker（只处理其中未映射的自动行）。
  - 设置（`GET/PUT /admin/settings`，组「决策模型」）：`clef.enabled`（bool，默认 false，控制后台 worker）、`clef.account_id`、`clef.api_token`（secret：GET 只返回 `is_set`，空串表示不修改；需要 Workers AI 权限）、`clef.model`（`clef-flash` 默认 / `clef`）、`clef.base_url`（默认 `https://api.cloudflare.com/client/v4`，可指向本地模拟服务）、`clef.metric`（`prob` 默认 / `confidence`）、`clef.apply_threshold`（0.8）、`clef.apply_margin`（0.3）、`clef.review_threshold`（0.5）、`clef.rate_per_min`（60，1…300）、`clef.daily_cap`（2000，UTC 日）。
- `GET /admin/plays?page=`：播放/推送记录
- `GET /admin/audit?page=`
- `GET /admin/stats`：`{users, plays_24h, qb_online, qb_total, pending_reviews}`

### 抓取源 `/admin/sources`（仅 admin）
后台定时抓取的种子来源（只抓第 1 页；条件 GET：ETag / If-Modified-Since；同一站点串行、请求间隔 ≥1.5 秒；UA `onlinetorrent/2 (+站点地址)`；Feed ≤10MB、.torrent ≤5MB）。默认内置：bangumi.moe（受信，启用）、蜜柑计划 RSS（启用）、Nyaa 1_3（启用）、动漫花园、ACG.RIP（关闭）。全局开关见设置 `crawl.enabled`。
入库流水线（每个 infohash 只做一次）：有 .torrent → 下载并本地解析（v1 infohash + 文件列表）；只有磁力 → qB 元数据队列（分类 `<qB 分类>-meta`、`stopCondition=MetadataReceived`，并发 3、单个 2 分钟超时、最多 3 次、状态存库可续跑，取到文件后删除本站添加的该种子）；然后文件分类（`kind`：正片 episode，特别篇 sp/op(NCOP)/ed(NCED)/pv/cm 保留编号并映射到集，Menu/Sample/Preview/特典为 extra 不映射）+ 刮削（bangumi.moe 标签强匹配，标题/别名匹配）。

Source：
```json
{ "id": 3, "name": "Nyaa", "kind": "rss|bangumi_moe", "url": "https://nyaa.si/?page=rss&c=1_3&f=0",
  "interval_min": 15,            // 5 … 10080
  "enabled": true,
  "proxy_disabled": false,       // true = 不走「上游代理」设置（也不走环境变量代理），直连
  "last_run_at": "…|null", "last_ok_at": "…|null",
  "last_items": 75,              // 上次成功抓取时 Feed 中的条目数（304 时不变）
  "last_new": 41,                // 上次新收录（含进入元数据队列）的种子数
  "last_error": "",              // 上次失败原因（成功后清空）
  "fail_count": 0,               // 连续失败次数：下次抓取间隔 = interval × 2^fail_count（最多 6 小时）
  "running": false,              // 正在抓取
  "next_run_at": "…|null",       // 预计下次抓取（停用或从未抓取时为 null）
  "created_at": "…", "updated_at": "…" }
```
- `GET /admin/sources` → `{"items": [Source]}`
- `GET /admin/sources/schema` → `{"kinds":[{value,label,description,example_url}], "min_interval_min":5, "default_interval_min":15}`
- `POST /admin/sources/probe` `{kind?="rss", url, proxy_disabled?}` → 不入库，只测试：
  `{"title":"Feed 标题","link":"…","format":"rss 2.0|atom 1.0|bangumi.moe JSON","item_count":75,"samples":[{"title","url?","published_at?","infohash?","torrent_url?","magnet":bool}]}`（最多 3 条）；失败 400 `source_probe_failed`（含原因；没有可识别的种子条目也算失败）。
- `POST /admin/sources` `{name?, kind?="rss", url, interval_min?=15, enabled?=true, proxy_disabled?=false}` → 201 Source。先做 probe，失败 400 `source_probe_failed`；`name` 省略时取 Feed 标题。地址重复 409 `source_exists`；参数错误 400。
- `PUT /admin/sources/id/{id}`（部分更新：只改出现的字段，字段同上）→ Source。改了 `url`/`kind` 会先 probe；改 `url`/`kind`/`proxy_disabled` 会清空条件 GET 缓存并立即到期；从停用改为启用也会立即到期。
- `DELETE /admin/sources/id/{id}` → `{"items": [Source]}`（已收录的种子保留）
- `POST /admin/sources/id/{id}/run` → 202 Source（`running: true`），后台立即抓取一次（无视间隔、启用状态与全局开关）；正在抓取时 409 `busy`。结果稍后见列表。
- `GET /admin/bgm-archive` → `{dump, dump_at, digest, synced_at, subjects, episodes, relations, duration_sec, checked_at, error?, error_at?, running, phase?}`（bangumi Archive 同步状态；phase 为 subjects|episodes|relations）；`POST /admin/bgm-archive/run` → 202（即使未更新也重新导入），运行中 409 `busy`。设置 `bgm_archive.enabled` 控制每 6 小时自动检查。
- `GET /admin/metadata-jobs` → `{"items":[{infohash, source_id, status:"pending|running|done|failed", attempts, next_at, server_id, added, started_at, last_error, created_at, updated_at}]}`（最近 200 条，未完成的在前）

相关设置（`GET/PUT /admin/settings`）：`bt.trackers`（string_list，默认 opentrackr / torrent.eu.org / stealth.si / desync / nyaa.tracker.wf / explodie，2026-10 实测可 scrape）、`swarm.enabled`（bool，默认 true）、`crawl.enabled`（bool，默认 true）、`crawl.workers`（同时抓取的源数，默认 2）、`upstream.tmdb_api_key`（secret，默认空；TMDB v3 key 或 v4 读取令牌）、`upstream.proxy`（抓取与 bgm.tv/TMDB 请求共用；为空时使用环境变量 `HTTPS_PROXY` 等）。

## 映射服务 `/meta/v1/*`（metad，可独立部署；默认与 otd 同进程挂载）
映射粒度是**文件级**：`(infohash, file_index)` → 作品 + 集。种子的每个文件一行（含字幕、字体等，`kind=other`）。`episode`（正片）与特别篇 `sp`/`op`(NCOP)/`ed`(NCED)/`pv`/`cm` 映射到作品 + 集，`episode` 列存正片集数或特别篇编号（没有编号的特别篇为 1；如 `NCOP2` → `kind=op, episode=2`）；Menu/Sample/Preview/特典（`extra`）与非视频（`other`）不映射。集的键由 `kind` + `episode` 得出（`op` → `ncop`，`ed` → `nced`）。
- 迁移 5（2026-10）：`meta_state` 表 + 一次性重新分类：otd / metad 启动时在后台对已存储的文件路径重跑 `ClassifyFile`（只改 `status=auto` 的行；作品沿用该种子已映射的作品，种子原来没有作品且有文件新变为可映射时重新匹配），完成后标记 `reclassify_specials_v1=done`，之后不再执行。
- 迁移 9（2026-10）：决策模型 `clef_jobs(infohash PK, status, attempts, requests, next_at, last_error, created_at, updated_at)`、`clef_decisions(id, infohash, file_index, path, work_key, kind, episode, episode_key, score, margin, confidence, answers JSON, model, status, reason, created_at, decided_at, decided_by)`。
- 迁移 7（2026-10）：`torrent_metainfo(infohash PK, data BLOB, source crawl|qb|bangumi.moe, created_at)`：种子文件原文（写入前校验 bencode、≤ 5 MB、v1 infohash 一致），见 `GET /api/v1/torrents/hash/{ih}/torrent`。

- `GET /meta/v1/torrents/hash/{infohash}` →
```json
{ "infohash": "...", "title": "...", "status": "ready|pending",
  "files": [{ "index": 0, "path": "[Team] Foo - 02 [1080p].mkv", "size_bytes": 0,
              "kind": "episode|sp|op|ed|pv|cm|extra|other",
              "work": {"key": "bgm:123", "title": "...", "title_cn": "...", "external": {"bgm": 123, "mal": 1}},
              "episode": 2,               // 正片集数或特别篇编号
              "episode_key": "2",         // 可省略：可映射的 kind 且有编号时（"12.5"、"sp1"、"ncop1"…）
              "confidence": 0.95, "status": "auto|verified|rejected", "method": "moe_tag+name" }] }
```
- `method`：规则映射为 `moe_tag|title|alias[+type|+season|+folder][+name]`；决策模型映射为 `clef`（之后的自动匹配不覆盖）；人工纠正为 `manual`。
- 2026-10 起规则只映射确定的文件；不确定的视频文件不映射（`work` 为 null，可能带解析出的 `episode` / `episode_key`），并进入决策模型队列。已有的映射不重新处理。
- 在 otd 内挂载时，读取接口按当前用户的内容范围处理（范围之外的条目 404、不出现在搜索中、文件映射中不给出 `work` / `episode`）；独立部署的 metad 没有用户，不受限制。
- `GET /meta/v1/subjects/{provider}/{id}`（含 external ids）/ `GET /meta/v1/subjects/search?q=标题`
- `GET /meta/v1/subjects/{provider}/{id}/episodes` → `{"items":[{key, kind, number, label, files}]}`：有映射文件的集（不含没有文件的已播正片）
- `GET /meta/v1/subjects/{provider}/{id}/episodes/{key}/files` → `{"key": "ncop1", "items": [...]}`：该集所有已知文件（infohash + file_index + 种子标题）；`{key}` 为集的键，无效 400
- `PUT /meta/v1/torrents/hash/{infohash}/files/{index}`（admin）`{subject_id|work_key, episode_key?, episode?, kind?, status?}` 手工纠正：给出 `episode_key`（如 `"ncop1"`）时由它决定 `kind` + `episode`；否则 `kind` 默认 `episode`，须为 `episode|sp|op|ed|pv|cm|extra|other`（无效 400）；`status` 默认 `verified`（verified 后自动匹配不再覆盖）
