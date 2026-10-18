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

## 路径约定（同 YotsubaNews）
资源/维度/值，纯路径；只有自由文本搜索、分页等筛选才用 `?`。

**前端页面**
| 页面 | 路径 |
|---|---|
| 首页（新番时间表） | `/` |
| 播放（自动选视频文件） | `/watch/hash/{infohash}` |
| 播放指定文件 | `/watch/hash/{infohash}/file/{index}` |
| 条目（单季） | `/subject/{provider}/{id}`：`id`（本站数字 ID）、`bgm`、`mal`、`anidb`、`anilist`、`tmdb`… |
| 单集（自动选资源播放） | `/subject/{provider}/{id}/episode/{n}` |
| 搜索 | `/search/{keyword}`（翻页 `?page=`） |
| 登录 / 注册 / 邀请注册 | `/login`（`?next=`）/ `/register` / `/register/invite/{code}` |
| 个人中心 / 后台 | `/me` / `/admin/...` |
| 系列（整个系列，TODO） | `/series/{provider}/{id}` |
旧链接兼容：`/?uri=磁力`、`/watch?uri=|ih=|t=`、`/anime/{key}` 均重定向到新路径。

**API**
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/schedule` | 新番时间表 |
| GET | `/api/v1/subjects/{provider}/{id}` | 条目详情 + 本地种子（映射到该条目的） |
| GET | `/api/v1/subjects/{provider}/{id}/episodes/{n}` | 单集所有已知文件（含当前用户 access），`items[0]` 为自动推荐 |
| GET | `/api/v1/search/{keyword}` | 搜索：番剧 + 种子（本地库；翻页 `?page=`） |
| GET | `/api/v1/torrents/hash/{ih}` | 种子收录状态与文件刮削结果（做种后轮询） |
| POST | `/api/v1/torrents/hash/{ih}/play` | 打开种子（策略判定 + 磁力） |
| POST | `/api/v1/torrents/moe/{id}/play` | 按 bangumi.moe ID 打开（本地库中由 bangumi.moe 抓取源收录的种子） |
| POST | `/api/v1/torrents/hash/{ih}/seed` / `report` | 服务器做种 / 举报 |
| GET | `/api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]` | WebSeed 就绪推送（SSE） |
| GET | `/webseed/hash/{ih}/[文件路径]` | HTTP WebSeed 文件（Cookie 鉴权） |
| GET | `/meta/v1/torrents/hash/{ih}` | 文件级映射 |
| PUT | `/meta/v1/torrents/hash/{ih}/files/{idx}` | 纠正映射（admin），body `{subject_id|work_key, episode, kind, status}` |
| GET | `/meta/v1/subjects/{provider}/{id}` / `.../episodes/{n}/files` / `/meta/v1/subjects/search?q=` | 映射库条目 |
| * | `/api/v1/admin/{resource}/id/{id}` | 后台单项资源 |
| GET/POST | `/api/v1/admin/sources`，`/admin/sources/schema`，`/admin/sources/probe` | 抓取源列表 / 新建 / 类型说明 / 测试 |
| PUT/DELETE | `/api/v1/admin/sources/id/{id}`，POST `…/id/{id}/run` | 抓取源修改 / 删除 / 立即抓取 |

> 已移除：`GET /api/v1/torrents/search?q=`（改为 `GET /api/v1/search/{keyword}`）。用户请求路径上不再实时请求 bangumi.moe：搜索、条目种子列表、时间表最新一集全部读本地库（由后台抓取源填充，见下方「抓取源」）。

## 公共

### `GET /site`
```json
{ "name": "Project Kawaii", "announcement_md": "...markdown...",
  "registration": "open|invite|closed", "turnstile_site_key": "",
  "trackers": ["wss://tracker.projectk.org", "..."],
  "ice_servers": [{"urls": ["stun:stun.l.google.com:19302"]}],
  "max_auto_size_bytes": 2147483648 }
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
    "latest_torrent": null,         // 或 Torrent（见下）：本地库中映射到该番的最新一集里自动推荐的种子（受信来源、AVC、1080p、大小合适优先）
    "latest_episode": 2             // 映射库已知的最新一集（可为 null）→ 前端卡片直达 /subject/…/episode/{n}
  }]
}
```

### `GET /subjects/{provider}/{id}`
```json
{ "subject": { ...同 schedule item..., "summary": "简介", "type": "tv", "official_site": "https://…", "tags": ["..."],
               "external": {"bgm": "123", "mal": "1", "bangumi_moe": "5ff…"} },   // external 的值目前为字符串
  "torrents": [Torrent, ...] }     // 本地库中至少有一个文件映射到该条目的种子，按发布时间倒序，最多 100 个
```

### `GET /subjects/{provider}/{id}/episodes/{n}`
```json
{ "subject": { ...同上... }, "episode": 3,
  "items": [{ "infohash": "...", "file_index": 0, "path": "[Team] Foo - 03 [1080p].mkv", "size_bytes": 0,
              "torrent_title": "...", "team": "字幕组名", "published_at": "...", "source_id": "来源条目 ID（bangumi.moe _id / RSS guid），可空",
              "source": "抓取源名称（bangumi.moe、Nyaa…）或 user",
              "confidence": 0.85, "access": {"action": "allow", "reason": ""} }] }
```
`items[0]` 为服务端自动推荐；无已知文件时 `items` 为空数组。

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
  "seeders": 0, "leechers": 0,                 // 不再实时获取，恒为 0（保留字段）
  "access": { "action": "allow|login|vip|block", "reason": "" }
}
```
受信：后台把抓取源标记为「受信」（默认只有 bangumi.moe）后，其种子与原来的 bangumi.moe 种子一样不受「用户自填磁力最低权限」限制；未受信来源（如 Nyaa、蜜柑）的种子与用户磁力同等对待（默认需登录）。

### `GET /torrents/hash/{ih}`
种子在本地库中的收录状态与逐文件刮削结果，供播放页在「服务器做种」后轮询（建议 3–5 秒一次）。只读本地数据，不发起任何外部请求。接口权限可配置，默认游客可用。
```json
{ "status": "unknown|pending|ready",
  "title": "种子标题（本地库 / qB 元数据名称；未知时为空串）",
  "files": [{ "index": 0, "path": "目录/文件名.mkv", "size_bytes": 0,
              "kind": "episode|sp|op|ed|extra|other",
              "subject": { "subject_id": 1034, "bgm_id": 123456, "title": "原名", "title_cn": "中文名" },   // 可省略：未刮削到条目；bgm_id 可省略
              "episode": 2 }] }                                                                         // 可省略：仅在有 subject 时给出；可为小数（12.5）
```
- `ready`：文件列表已知（`files` 为全部文件，包含字幕、字体等 `other`）。`pending`：正在获取元数据（抓取源的磁力在 qB 元数据队列中、刚请求过服务器做种 30 分钟内、或做种后的检查进行中），`files` 为 `[]`。`unknown`：本地没有该种子，或获取失败（`title` 可能仍有值）。
- 前端：若当前播放文件刮削到 `subject` + `episode`，可切到 `/subject/id/{subject_id}/episode/{episode}/hash/{ih}/file/{index}`。

### `POST /torrents/hash/{ih}/play` / `POST /torrents/moe/{id}/play`（无 body）
服务端：解析 → 策略判定 → 返回带 wss tracker 的磁力（**不再推送给 qB**，见下方 seed）。默认游客可用。本地库已收录的种子（抓取源）使用库中的标题、文件列表与受信标记；`moe/{id}` 只查本地库（bangumi.moe 抓取源收录过的），找不到返回 404。
- 200：
```json
{ "access": {"action":"allow","reason":""},
  "infohash": "...", "name": "...", "magnet": "magnet:?xt=...&tr=wss://...",
  "seeded_by": [], "webseeds": [] ,           // 恒为空，保留字段兼容
  "meta": { "subject_id": 1034, "subject_key": "bgm:123", "bgm_id": "123", "title": "…", "episode": 2, "file_index": 0 } }   // meta 可空
```
- 401 `{"error":{"code":"login_required"}}`，402 `{"error":{"code":"vip_required"}}`，451 `{"error":{"code":"blocked","message":"原因"}}`。
- 用户磁力里的 `tr=` 一律丢弃（只用站点配置的 tracker）；`dn=` 只作为未受信标题参与判定（用户磁力最低权限始终生效），不写入种子标题，真实标题以 qB 元数据为准。
- 用户提交的种子获取元数据后会被复查；此外后台每 10 分钟用 qB 中的真实名称与文件列表复查本站分类下所有种子，命中屏蔽规则或超过大小上限的会被移除。

### `POST /torrents/hash/{ih}/seed` → `{"seeded_by": ["ams"]}`
让所有在线 qB 下载并做种（消耗服务器带宽 / 磁盘）。默认仅会员及以上。种子须先经 play 打开过；会对当前用户重新做策略判定。超过 `play.max_push_size_mb` 返回 413 `too_large`。
qB 拿到元数据后，后端把文件列表写入本地库并刮削（每个 infohash 一次）；期间 `GET /torrents/hash/{ih}` 返回 `pending`，完成后 `ready`。若该种子正好在抓取源的元数据队列里（qB 分类 `projectk-meta`），会被转入做种分类并开始下载，不会被队列删除。

### HTTP WebSeed（固定地址，Cookie 鉴权）
- 文件：`GET /webseed/hash/{ih}/`（单文件种子）或 `/webseed/hash/{ih}/{种子内文件路径}`（多文件，webtorrent 自动拼接）。
  不带签名/过期时间；每次请求用登录 Cookie 校验接口权限 `GET /webseed/hash/{ih}/{path...}`（后台「接口权限」可改，默认会员）+ 当前用户对该种子的策略。只提供 qB 已完整下载的文件，否则 404。`Cache-Control: private, no-store`。
- 就绪推送（SSE）：`GET /api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]`（种子须先经 play 打开）。事件：`waiting {progress}`（进度变化时）、`ready {webseeds:[固定地址]}`、`unavailable {reason: disabled|not_seeded}`、`timeout {}`（30 分钟）；每 25 秒一条 `: ping` 心跳；每 IP 最多 6 条并发连接。
- 前端：`/site.webseed = {enabled, delay_sec}`；播放开始 `delay_sec` 秒后订阅推送，收到 `ready` 才 `addWebSeed`，不做客户端探测。

### `POST /torrents/hash/{ih}/report` `{reason}` → `{}`  需登录。举报 → 进入审核队列（标题取自服务端记录，不接受客户端提供；reason 最长 500 字）

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
- `GET /admin/plays?page=`：播放/推送记录
- `GET /admin/audit?page=`
- `GET /admin/stats`：`{users, plays_24h, qb_online, qb_total, pending_reviews}`

### 抓取源 `/admin/sources`（仅 admin）
后台定时抓取的种子来源（只抓第 1 页；条件 GET：ETag / If-Modified-Since；同一站点串行、请求间隔 ≥1.5 秒；UA `onlinetorrent/2 (+站点地址)`；Feed ≤10MB、.torrent ≤5MB）。默认内置：bangumi.moe（受信，启用）、蜜柑计划 RSS（启用）、Nyaa 1_3（启用）、动漫花园、ACG.RIP（关闭）。全局开关见设置 `crawl.enabled`。
入库流水线（每个 infohash 只做一次）：有 .torrent → 下载并本地解析（v1 infohash + 文件列表）；只有磁力 → qB 元数据队列（分类 `<qB 分类>-meta`、`stopCondition=MetadataReceived`，并发 3、单个 2 分钟超时、最多 3 次、状态存库可续跑，取到文件后删除本站添加的该种子）；然后文件分类（`kind`，NCOP/NCED/PV/CM/Menu/Sample/特典不映射到集）+ 刮削（bangumi.moe 标签强匹配，标题/别名匹配）。

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
- `GET /admin/metadata-jobs` → `{"items":[{infohash, source_id, status:"pending|running|done|failed", attempts, next_at, server_id, added, started_at, last_error, created_at, updated_at}]}`（最近 200 条，未完成的在前）

相关设置（`GET/PUT /admin/settings`）：`crawl.enabled`（bool，默认 true）、`crawl.workers`（同时抓取的源数，默认 2）、`upstream.tmdb_api_key`（secret，默认空；TMDB v3 key 或 v4 读取令牌）、`upstream.proxy`（抓取与 bgm.tv/TMDB 请求共用；为空时使用环境变量 `HTTPS_PROXY` 等）。

## 映射服务 `/meta/v1/*`（metad，可独立部署；默认与 otd 同进程挂载）
映射粒度是**文件级**：`(infohash, file_index)` → 作品 + 集。种子的每个文件一行（含字幕、字体等，`kind=other`）；只有 `episode`/`sp` 会映射到作品+集，NCOP/NCED（`op`/`ed`）、PV/CM/Menu/Sample/特典（`extra`）不映射。

- `GET /meta/v1/torrents/hash/{infohash}` →
```json
{ "infohash": "...", "title": "...", "status": "ready|pending",
  "files": [{ "index": 0, "path": "[Team] Foo - 02 [1080p].mkv", "size_bytes": 0,
              "kind": "episode|sp|op|ed|extra|other",
              "work": {"key": "bgm:123", "title": "...", "title_cn": "...", "external": {"bgm": 123, "mal": 1}},
              "episode": 2, "confidence": 0.95, "status": "auto|verified|rejected", "method": "moe_tag+name" }] }
```
- `GET /meta/v1/subjects/{provider}/{id}`（含 external ids）/ `GET /meta/v1/subjects/search?q=标题`
- `GET /meta/v1/subjects/{provider}/{id}/episodes/{n}/files` → 该集所有已知文件（infohash + file_index + 种子标题）
- `PUT /meta/v1/torrents/hash/{infohash}/files/{index}`（admin）`{subject_id|work_key, episode, kind, status}` 手工纠正（status=verified 后自动匹配不再覆盖）
