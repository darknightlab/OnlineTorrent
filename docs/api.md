# onlinetorrent v2 API 契约（v1）

- Base: `/api/v1`，JSON，UTF-8。错误：`{"error":{"code":"string","message":"人类可读"}}` + 合适的 HTTP 状态码。
- 认证：登录后服务端设置 HttpOnly Cookie `ot_session`（SameSite=Lax）。写操作需带请求头 `X-Requested-With: fetch`（简易 CSRF 防护）。
- 时间：RFC3339 字符串（UTC）。大小：字节 `int64`。
- 角色：`guest`（未登录）< `member` < `vip` < `admin`。

## 公共

### `GET /site`
```json
{ "name": "Project Kawaii", "announcement_md": "...markdown...",
  "registration": "open|invite|closed", "turnstile_site_key": "",
  "trackers": ["wss://tracker.projectk.org", "..."],
  "ice_servers": [{"urls": ["stun:stun.l.google.com:19302"]}],
  "max_auto_size_bytes": 2147483648 }
```

### `GET /me` → `{"user": null}` 或
```json
{ "user": { "id": 1, "username": "a", "email": "a@x", "role": "member", "vip_until": null, "created_at": "..." } }
```

### `POST /auth/register` `{username, email, password, invite_code?, turnstile_token?}` → `/me` 结构（并登录）
### `POST /auth/login` `{identifier, password}` → `/me` 结构
### `POST /auth/logout` → `{}`
### `POST /me/password` `{old_password, new_password}` → `{}`
### `POST /me/redeem` `{code}` → `/me` 结构（兑换码 → vip）

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

### `GET /torrents/latest?page=1` / `GET /torrents/search?q=xxx&page=1`
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
服务端：解析 → 策略判定 → 允许则推送给 qB 池做种 → 返回带 wss tracker 的磁力。
- 200：
```json
{ "access": {"action":"allow","reason":""},
  "infohash": "...", "name": "...", "magnet": "magnet:?xt=...&tr=wss://...",
  "seeded_by": ["ams"], "webseeds": [] ,
  "meta": { "anime_key": "bgm:123", "episode": 2 } }   // meta 可空
```
- 401 `{"error":{"code":"login_required"}}`，402 `{"error":{"code":"vip_required"}}`，451 `{"error":{"code":"blocked","message":"原因"}}`。

### `POST /report` `{infohash, reason}` → `{}`  举报 → 进入审核队列

## 后台 `/admin/*`（仅 admin）
- `GET/PUT /admin/settings` 全部站点设置（KV 对象，字段见 `GET` 返回）
- `GET/POST /admin/qb-servers`，`PUT/DELETE /admin/qb-servers/{id}`，`POST /admin/qb-servers/{id}/test`
  QBServer: `{id,name,url,username,password(写入时),api_key(写入时),category,enabled,seeding_time_limit_min,ratio_limit,max_active,online,version,last_error,last_check_at}`
- `GET /admin/users?q=&page=`，`PUT /admin/users/{id}` `{role?, vip_until?, disabled?}`
- `GET/POST /admin/invites`、`GET/POST /admin/redeem-codes`
- `GET/POST /admin/rules`，`PUT/DELETE /admin/rules/{id}`
  Rule: `{id, name, enabled, priority, match: {"field":"title|file|infohash|team|tag|category","op":"contains|regex|equals","value":"..."}, action:"allow|login|vip|block", label:"political|porn|copyright|other|", note}`
- `POST /admin/policy/test` `{title, infohash?, files?, tags?, team?}` → `{access, matched_rule}`
- `GET /admin/reviews?status=pending` / `PUT /admin/reviews/{id}` `{action, label, note}`（写入 infohash 级裁决）
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
