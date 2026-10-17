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
  "permissions": ["GET /api/v1/schedule", "POST /api/v1/play", "POST /api/v1/torrents/{ih}/seed", "..."] }
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
    "key": "bgm:123456",          // 稳定标识：优先 bgm:{subjectId}，否则 bd:{bangumi-data 索引名}
    "title": "葬送のフリーレン",   // 原名
    "title_cn": "葬送的芙莉莲",    // 可能为空
    "cover": "https://.../cover.jpg", // 可能为空，前端要有占位
    "begin": "2026-10-03T14:00:00Z",
    "weekday": 6,                  // 0=周日 … 6=周六，按 Asia/Shanghai 计算
    "air_time": "22:00",           // Asia/Shanghai 时间
    "last_air_at": "2026-10-10T14:00:00Z",
    "next_air_at": "2026-10-17T14:00:00Z",
    "episodes_aired": 2,           // 估算
    "bgm_id": 123456, "moe_tag_id": "5ff…",
    "latest_torrent": null          // 或 Torrent（见下），bangumi.moe 该 tag 最新一个
  }]
}
```

### `GET /anime/{key}`
```json
{ "anime": { ...同 schedule item..., "summary": "简介", "tags": ["..."], "external": {"bgm": 123, "mal": 1, "anidb": 2, "tmdb": null, "imdb": null} },
  "torrents": [Torrent, ...] }
```

### `GET /torrents/search?q=xxx&page=1`
`page` 上限 50（超出按 50 处理）；`q` 最长 100 字；每 IP 突发 20 次、之后每 3 秒 1 次。
```json
{ "page": 1, "page_count": 30, "items": [Torrent, ...] }
```
Torrent：
```json
{ "id": "bangumi.moe 的 _id，或 ih:{infohash}",
  "infohash": "40位小写hex",
  "title": "[字幕组] xxx - 02 [1080p]",
  "size_bytes": 367001600, "files": 1,
  "team": {"id": "...", "name": "..."} ,      // 可空
  "tags": [{"id": "...", "name": "..."}],
  "published_at": "...",
  "source": "bangumi.moe|user",
  "access": { "action": "allow|login|vip|block", "reason": "" }
}
```

### `POST /play` `{ "magnet": "magnet:?..." }` 或 `{ "infohash": "..." }` 或 `{ "torrent_id": "bangumi.moe id" }`
服务端：解析 → 策略判定 → 返回带 wss tracker 的磁力（**不再推送给 qB**，见下方 seed）。默认游客可用。
- 200：
```json
{ "access": {"action":"allow","reason":""},
  "infohash": "...", "name": "...", "magnet": "magnet:?xt=...&tr=wss://...",
  "seeded_by": [], "webseeds": [] ,           // 恒为空，保留字段兼容
  "meta": { "anime_key": "bgm:123", "episode": 2 } }   // meta 可空
```
- 401 `{"error":{"code":"login_required"}}`，402 `{"error":{"code":"vip_required"}}`，451 `{"error":{"code":"blocked","message":"原因"}}`。
- 用户磁力里的 `tr=` 一律丢弃（只用站点配置的 tracker）；`dn=` 只作为未受信标题参与判定（用户磁力最低权限始终生效），不写入种子标题，真实标题以 qB 元数据为准。
- 用户提交的种子获取元数据后会被复查；此外后台每 10 分钟用 qB 中的真实名称与文件列表复查本站分类下所有种子，命中屏蔽规则或超过大小上限的会被移除。

### `POST /torrents/{ih}/seed` → `{"seeded_by": ["ams"]}`
让所有在线 qB 下载并做种（消耗服务器带宽 / 磁盘）。默认仅会员及以上。种子须先经 `/play` 打开过；会对当前用户重新做策略判定。超过 `play.max_push_size_mb` 返回 413 `too_large`。

### `POST /torrents/{ih}/webseed` → `{"webseeds": ["https://www.projectk.org/webseed/{exp}.{sig}/{ih}/"], "delay_sec": 8}`
签名的 HTTP WebSeed 地址（6 小时有效，只服务 qB 已完整下载的文件）。默认仅会员及以上；后台关闭 WebSeed 时返回空数组。前端应在 `delay_sec` 秒后再加入，优先 P2P。

### `POST /report` `{infohash, reason}` → `{}`  需登录。举报 → 进入审核队列（标题取自服务端记录，不接受客户端提供；reason 最长 500 字）

## 后台 `/admin/*`（仅 admin）
- `GET /admin/permissions` → `{"roles":["guest","member","vip"], "items":[{key, method, path, group, label, access, default_roles, granted:{guest,member,vip}}]}`
- `PUT /admin/permissions` `{"grants": {"POST /api/v1/torrents/{ih}/seed": ["member"], ...}}` → 同 GET（只能改 configurable / user 类型；user 类型不能授权给 guest）
- `GET/PUT /admin/settings` 全部站点设置（KV 对象，字段见 `GET` 返回）
- `GET/POST /admin/qb-servers`，`PUT/DELETE /admin/qb-servers/{id}`，`POST /admin/qb-servers/{id}/test`
  QBServer: `{id,name,url,username,password(写入时),api_key(写入时),category,enabled,seeding_time_limit_min,ratio_limit,max_active,online,version,last_error,last_check_at}`
- `GET /admin/users?q=&page=`，`PUT /admin/users/{id}` `{role?, vip_until?, disabled?}`（设 `role: "vip"` 且不传 `vip_until` = 永久大会员，会清空 vip_until）
- `GET/POST /admin/invites`、`GET/POST /admin/redeem-codes`
  创建邀请 `{max_uses?, role, vip_days, expires_in_days, note}`：`max_uses` 省略为 1，0 为不限，负数 400；`vip_days` 需 ≥ 0。邀请码大写，使用时不区分大小写。
- `GET/POST /admin/rules`，`PUT/DELETE /admin/rules/{id}`
  Rule: `{id, name, enabled, priority, match: {"field":"title|file|infohash|team|tag|category","op":"contains|regex|equals","value":"..."}, action:"allow|login|vip|block", label:"political|porn|copyright|other|", note}`
- `POST /admin/policy/test` `{title, infohash?, files?, tags?, team?}` → `{access, matched_rule}`
- `GET /admin/reviews?status=pending` / `PUT /admin/reviews/{id}` `{action, label, note}`（写入 infohash 级裁决；action 为 block 或 label 为 political/porn 时同时从 qB 移除。note 仅后台可见，前台理由由 action+label 生成）
- 日志保留：plays / audit 保留 90 天，已处理的 reviews 保留 180 天（每日清理）。
- `GET /admin/plays?page=`：播放/推送记录
- `GET /admin/audit?page=`
- `GET /admin/stats`：`{users, plays_24h, qb_online, qb_total, pending_reviews}`

## 映射服务 `/meta/v1/*`（metad，可独立部署；默认与 otd 同进程挂载）
映射粒度是**文件级**：`(infohash, file_index)` → 作品 + 集。合集种子每个视频文件一行；字幕/字体/NCOP 等不映射或标记 `kind`。

- `GET /meta/v1/torrents/{infohash}` →
```json
{ "infohash": "...", "title": "...",
  "files": [{ "index": 0, "path": "[Team] Foo - 02 [1080p].mkv", "size_bytes": 0,
              "kind": "episode|sp|op|ed|extra|other",
              "work": {"key": "bgm:123", "title": "...", "title_cn": "...", "external": {"bgm": 123, "mal": 1}},
              "episode": 2, "confidence": 0.95, "status": "auto|verified|rejected", "method": "moe_tag+name" }] }
```
- `GET /meta/v1/works/{key}`（含 external ids）/ `GET /meta/v1/works?external=mal:123` / `GET /meta/v1/works?q=标题`
- `GET /meta/v1/works/{key}/episodes/{n}/files` → 该集所有已知文件（infohash + file_index + 种子标题）
- `PUT /meta/v1/torrents/{infohash}/files/{index}`（admin）`{work_key, episode, kind, status}` 手工纠正（status=verified 后自动匹配不再覆盖）
