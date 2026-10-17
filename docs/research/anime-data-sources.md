# 动画数据源调研（projectk.org 重建）

> 调研时间：2026-10-17（UTC），所有示例均为当天真实请求结果（已裁剪）。请求通过 `timeout 20 curl` 发起，次数有限。
> 目标：① 首页"新番时间线"（类似 https://ani.gamer.com.tw/ ）；② 点击剧集 → 找到 bangumi.moe 种子 → WebTorrent 播放；③ 维护 `infohash → 作品/剧集` 映射库（bgm.tv / AniDB / MAL / AniList / TMDB / TVDB / IMDb），服务于弹幕、封面、Sonarr。

---

## TL;DR

| 需求 | 首选数据源 | 备注 |
|---|---|---|
| 新番时间线（精确到分钟） | **bangumi-data**（`begin` + `broadcast: R/<ISO>/P7D`） | bgm.tv `/calendar` 只给星期，不给时刻 |
| 作品/剧集元数据、封面 | **bgm.tv API**（`/v0/subjects`、`/v0/episodes`；或 `next.bgm.tv/p1/*`） | 必须自定义 UA；api.bgm.tv 当天多次 52x |
| 种子 | **bangumi.moe API** | 列表即带 `magnet`/`infoHash`/文件列表/`tag_ids` |
| bangumi.moe tag → bgm.tv | **bangumi-data `sites[].site=="bangumi_moe"` 的 id 就是 bangumi.moe 的 bangumi tag `_id`** | 近一季覆盖率 132/161 |
| 其他 ID 互转 | Fribb/anime-lists（AniDB↔MAL↔AniList↔TMDB↔TVDB↔IMDb…）+ Anime-Lists XML（AniDB↔TVDB 季/集偏移） | 二者都**不含 bgm.tv**，需经 bangumi-data 的 anidb/mal 桥接 |
| 现成 "种子→bgm subject" 参考 | **AnimeGarden**（api.animes.garden，资源带 `subjectId`）、Mikan（以 infohash 为剧集页 key） | 可作为自动匹配的交叉校验 |
| 种子→AniDB 集 | animetosho（`anidb_aid/eid/fid`） | 当天 feed 最新条目停在 2026-05-08，疑似停更；不收录 bangumi.moe |
| 弹幕 | dandanplay 开放 API（需 AppId/AppSecret）；有 `/api/v2/bangumi/bgmtv/{id}` 直接按 bgm subject 查 | `match` 接口用"前 16MB MD5"，WebTorrent 可直接算 |
| 发布名解析 | anitomy 系（C++ 原版、anitogo、anitomy-rs、yjl9903/anitomy TS） | 中文字幕组命名需要额外规则 |

---

## 1. bangumi.moe API

Base：`https://bangumi.moe`。无需鉴权（读）。端点清单来自前端 `https://static.bangumi.moe/scripts/rin.pr.min.js` 并实测。

### 1.1 已验证端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/torrent/latest` | 最新 30 条，含 `introduction`(HTML) |
| GET | `/api/torrent/page/{n}` | 分页（每页 30），返回 `page_count`（当天 7641 页） |
| GET | `/api/v2/torrent/page/{n}` | 同上但**不含 introduction**，更轻量 ✅推荐 |
| GET | `/api/v2/torrent/{_id}` | 种子详情（含 introduction）|
| POST | `/api/torrent/fetch` `{"_id":"..."}` | 详情，额外有 `titleIndex` |
| GET | `/api/v2/torrent/search?query=xx&p=1` | 关键字搜索，返回 `count/page_count/torrents` |
| POST | `/api/torrent/search` `{"tag_id":[...],"p":1}` | **按 tag（番剧/字幕组/格式）交集过滤** ✅ |
| GET | `/api/bangumi/current`、`/api/bangumi/recent` | 本季番剧（当天 69 条），含 `tag_id`、`showOn`(星期)、`startDate/endDate`(ms)、`icon/cover`、`acgdb_id` |
| GET | `/api/bangumi/timeline` | TimelineJS 格式的放送时间线（带具体时刻） |
| POST | `/api/tag/fetch` `{"_ids":[...]}` | 批量把 `tag_ids` 解析为名称/类型/多语言 |
| POST | `/api/tag/search` `{"name":"朱色","keywords":true,"multi":true}` | 按名称找 tag |
| POST | `/api/tag/suggest` `{"query":"<标题>"}` | **给标题自动推荐 tag**（字幕组/分辨率/格式…），可用于解析 |
| GET | `/api/tag/popbangumi` `/api/tag/team` `/api/tag/common` `/api/tag/misc` | 各类 tag 列表 |
| POST | `/api/team/fetch` `{"_ids":[...]}` | 字幕组详情（`team_id` → 名称、`tag_id`）|
| GET | `/rss/latest`、`/rss/tags/{tagId}`、`/rss/search/{kw}` | RSS |

404：`/api/torrent/{id}`、`/api/team/working`（v1）、`/api/tag/{id}`。`/api/bangumi/search` 返回 `found:false`（似乎需要特定参数）。

### 1.2 种子对象（`/api/torrent/latest`，去掉 introduction）

```json
{
  "_id": "6ad38fb7992853e4c9849855",
  "category_tag_id": "549ef207fe682f7549f1ea90",
  "title": "[黒ネズミたち] 和沒有信徒的女神大人一起攻略異世界 / Shinja Zero no Megami-sama - 01 (ABEMA 1920x1080 AVC AAC MKV)",
  "tag_ids": ["63e4b7585fa12c0007949b88","548ee2ce4ab7379536f56358","549121df0b086a345379bed1",
              "55c3036c7030f8c855f992ee","548ee00b4ab7379536f56353","6abdf90398d9339c5c743170",
              "549ef207fe682f7549f1ea90"],
  "comments": 0, "downloads": 1, "finished": 0, "leechers": 0, "seeders": 0,
  "uploader_id": "60098e9132f14c000739f8f4",
  "team_id": "63e497a55fa12c0007944399",
  "publish_time": "2026-10-17T15:09:43.826Z",
  "magnet": "magnet:?xt=urn:btih:4c092aace051b607825bfa6a86735ecf4c40448c",
  "infoHash": "4c092aace051b607825bfa6a86735ecf4c40448c",
  "file_id": "6ad38fb7992853e4c9849854",
  "teamsync": null,
  "size": "746.77 MB",
  "btskey": "",
  "content": [["[Dynamis One] Shinja Zero no Megami-sama - 01 (ABEMA 1920x1080 AVC AAC MKV) [7CB4C9A3].mkv","746.77 MB"]]
}
```

要点：
- `magnet` 只有 btih、**没有 tracker**，WebTorrent 需自行补 WSS tracker（浏览器只能连 WebRTC peer，见末尾风险）。
- `content` = 文件列表 `[路径, 人类可读大小]`，大小是字符串，需要精确大小得读 .torrent（`/download/torrent/{_id}/...` 由前端拼接）。
- `tag_ids` 混合了 team / resolution / format / lang / bangumi / misc(category) 等类型，需 `/api/tag/fetch` 解析 `type`。
- `introduction` HTML 中常带 `bgm.tv/subject/621073`、`anilist.co/anime/…`、`myanimelist.net/anime/…`、`themoviedb.org/tv/…` 链接 —— **可作为高置信度映射线索**（发布者手填）。

### 1.3 Tag 对象（`/api/tag/fetch`）

```json
{"_id":"6abdf90398d9339c5c743170","name":"信者ゼロの女神サマと始める異世界攻略","type":"bangumi",
 "synonyms":["信者ゼロの女神サマと始める異世界攻略","和沒有信徒的女神大人一起攻略異世界","Shinja Zero no Megami-sama to Hajimeru Isekai Kouryaku", "..."],
 "locale":{"ja":"信者ゼロの女神サマと始める異世界攻略","zh_tw":"和沒有信徒的女神大人一起攻略異世界","en":"Shinja Zero no Megami-sama to Hajimeru Isekai Kouryaku","zh_cn":"与信徒为零的女神大人开始的异世界攻略"},
 "syn_lowercase":["..."]}
```
```json
{"_id":"548ee2ce4ab7379536f56358","name":"1080p","type":"resolution","synonyms":["1080p","1920x1080","1920*1080","1920×1080","1080","FullHD","1080P"]}
```

### 1.4 番剧对象（`/api/bangumi/current`）

```json
{"_id":"6abdf91398d9339c5c7431e3","name":"dreamland","credit":"Ellipse",
 "startDate":1792342800000,"endDate":1798038000000,"showOn":6,
 "tag_id":"6abdf91398d9339c5c7431e2",
 "icon":"data/images/2026/10/anime-2339-icon.jpeg","cover":"data/images/2026/10/anime-2339-cover.jpeg",
 "acgdb_id":"anime-2339"}
```
`/api/bangumi/timeline` 片段：
```json
{"timeline":{"type":"default","date":[{"startDate":"2026-10-17T02:30:00.000Z","headline":"<a href=\"/tag/548f0f2df892774b140ac674\">妖精的尾巴</a>","text":"A-1Pictures/ブリッジ","asset":{"media":"data/images/2014/12/6dznn2rykghikpgav2k.jpg"}}]}}
```

### 1.5 与 bgm.tv 的关系
- bangumi.moe 自身**不存 bgm.tv id**（`acgdb_id` 是其内部/ACGDB 编号，68/69 条有值，前端仅把 `anime-` 前缀去掉显示，无法直接用）。
- **桥接方式：bangumi-data 的 `sites` 中 `site:"bangumi_moe"` 的 `id` 正是 bangumi.moe 的 bangumi tag `_id`**（示例：`6abdf90398d9339c5c743170` ↔ bgm `621073`，见 §3）。
- 补充：introduction 内链接、AnimeGarden 的 `subjectId`。

---

## 2. bangumi.tv API

- 文档：https://bangumi.github.io/api/ （OpenAPI：https://bangumi.github.io/api/dist.json ）
- 读公开数据**无需 token**；需要用户数据/NSFW 条目时用 `Authorization: Bearer <token>`，token 在 https://next.bgm.tv/demo/access-token 生成。
- **User-Agent 政策**（https://github.com/bangumi/api/blob/master/docs-raw/user%20agent.md ）：非浏览器调用必须带"开发者 ID/应用名（+版本）(+主页)"，如 `darknightlab/projectk (https://projectk.org)`；默认库 UA 可能被封。
- 速率：文档未给出明确数字；当天经代理访问 `api.bgm.tv` 多次返回 Cloudflare **521/522**（`/calendar`、`/v0/episodes`），`/v0/subjects/621073` 成功但耗时 20s。→ **必须本地缓存 + 低并发 + 失败退避**；可用 `next.bgm.tv/p1/*`（新版前端用的私有 API，非稳定契约）作为后备；全量数据可用每周 dump：https://github.com/bangumi/Archive/releases （如 `dump-2025-06-10.210258Z.7z`，~190MB）。

主要端点：`GET /calendar`、`GET /v0/subjects/{id}`、`GET /v0/episodes?subject_id=&type=0&limit=&offset=`、`GET /v0/episodes/{id}`、`POST /v0/search/subjects`、`GET /v0/subjects/{id}/subjects`（关联条目，续作/季）。

`GET https://api.bgm.tv/v0/subjects/621073`（裁剪）：
```json
{"id":621073,"type":2,"name":"信者ゼロの女神サマと始める異世界攻略","name_cn":"和没有信徒的女神大人一起攻略异世界",
 "date":"2026-10-11","platform":"TV","total_episodes":12,"eps":0,
 "images":{"large":"https://lain.bgm.tv/pic/cover/l/ec/f9/621073_oFcZ3.jpg","common":"https://lain.bgm.tv/r/400/pic/cover/l/ec/f9/621073_oFcZ3.jpg"},
 "rating":{"rank":7081,"total":73,"score":5.6},
 "meta_tags":["TV","日本","奇幻","穿越","小说改"],
 "infobox":[{"key":"中文名","value":"和没有信徒的女神大人一起攻略异世界"},{"key":"放送开始","value":"2026年10月11日"},{"key":"放送星期","value":"星期日"}]}
```

`GET https://next.bgm.tv/p1/subjects/621073/episodes?limit=2`（v0 `/v0/episodes` 字段基本一致：`id, sort, ep, type, airdate, name, name_cn, duration`）：
```json
{"data":[{"id":1743421,"subjectID":621073,"sort":1,"type":0,"disc":0,"name":"高月マコト、信者になる","nameCN":"高月诚人成为信徒","duration":"00:23:40","airdate":"2026-10-11","comment":20}]}
```
- 剧集 `type`：0 本篇 / 1 SP / 2 OP / 3 ED …；`sort` 为条目内序号（续作季常从 13 开始等，**与发布名中的集号可能不一致**）。
- `/calendar` 只按星期分组（`next.bgm.tv/p1/calendar` 当天 7 天各 12/10/13/11/14/21/26 部），**没有具体播出时刻** → 不适合精确时间线。

---

## 3. bangumi-data

- 仓库：https://github.com/bangumi-data/bangumi-data （npm `bangumi-data`，当天 latest `0.3.231`）
- CDN：`https://cdn.jsdelivr.net/npm/bangumi-data@0.3/dist/data.json`（~7.9MB，8922 条，含 `siteMeta` + `items`）；备选 unpkg。
- 字段：`title, titleTranslate{zh-Hans,zh-Hant,en}, type(tv/web/movie/ova), lang, officialSite, begin, end, broadcast, comment, sites[]{site,id,begin?,broadcast?,regions?}`。
- `siteMeta` 共 39 站：`bangumi, anidb, mal, aniList, tmdb, bangumi_moe, mikan, dmhy, acgnx, gamer, bilibili, crunchyroll, netflix, …`（无 TVDB、IMDb）。
- 2026-07 之后 161 部：含 bangumi 161、bangumi_moe 132、mal 115、anidb 101、tmdb 95。

实例：
```json
{
  "title": "信者ゼロの女神サマと始める異世界攻略",
  "titleTranslate": {"zh-Hans": ["和没有信徒的女神大人一起攻略异世界"], "zh-Hant": ["和沒有信徒的女神大人一起攻略異世界"]},
  "type": "tv", "lang": "ja",
  "begin": "2026-10-11T14:30:00.000Z",
  "broadcast": "R/2026-10-11T14:30:00.000Z/P7D",
  "end": "",
  "sites": [
    {"site": "bangumi", "id": "621073"},
    {"site": "bangumi_moe", "id": "6abdf90398d9339c5c743170"},
    {"site": "gamer", "id": "149262", "begin": "", "broadcast": ""},
    {"site": "crunchyroll", "id": "GT00383992", "begin": "2026-10-11T14:30:00.000Z", "broadcast": "R/2026-10-11T14:30:00.000Z/P7D"},
    {"site": "dmhy", "id": "沒有 信徒 女神"}
  ]
}
```
**是否适合时间线：是，首选。** `broadcast` 为 ISO 8601 重复区间（`R/起点/P7D`），第 n 集预计播出 = 起点 + (n-1)×7d；各站点 `sites[].broadcast` 给出网络配信时间（中国用户更关心 bilibili/gamer 时间）。缺点：停播/延期/特番不会体现 → 用 bgm.tv `episodes[].airdate` 校正，用"bangumi.moe 实际出现种子时间"标注"已有资源"。

---

## 4. 映射数据集 / 工具

### 4.1 manami-project/anime-offline-database
- https://github.com/manami-project/anime-offline-database ，每周 release（当天 `2026-27`），`anime-offline-database.jsonl(.zst)` 62MB / 41538 条，ODbL 许可。
- 只给 `sources` URL 列表（anidb/anilist/anime-planet/ann/kitsu/livechart/mal/simkl/anisearch…），**无 bgm.tv、无 TVDB/TMDB**；同一作品可能拆成多条（下例 3 条）。
```json
{"title":"Shinja Zero no Megami-sama to Hajimeru Isekai Kouryaku","type":"TV","episodes":1,"status":"UPCOMING","animeSeason":{"season":"FALL","year":2026},
 "sources":["https://anidb.net/anime/19822","https://anilist.co/anime/205896","https://livechart.me/anime/13477","https://myanimelist.net/anime/63292", "..."]}
```

### 4.2 Fribb/anime-lists ✅（ID 枢纽）
- https://github.com/Fribb/anime-lists ，`anime-list-full.json`（7.5MB），合并 manami + Anime-Lists XML。
```json
{"type":"TV","anidb_id":19822,"anilist_id":205896,"animecountdown_id":2990960,"animenewsnetwork_id":38274,
 "anisearch_id":21163,"imdb_id":["tt43650265"],"kitsu_id":51011,"livechart_id":13477,"mal_id":63292,
 "simkl_id":2990960,"themoviedb_id":{"tv":288588},"tvdb_id":472640,"season":{"tvdb":1,"tmdb":1}}
```
另有 `episode_offset` 字段。

### 4.3 Anime-Lists/anime-lists（ScudLee XML）✅（AniDB 集 → TVDB/TMDB 季集）
- https://github.com/Anime-Lists/anime-lists ，`anime-list-master.xml`，Plex HAMA/Kometa/Sonarr 生态的事实标准。
```xml
<anime anidbid="19822" tvdbid="472640" defaulttvdbseason="1" episodeoffset="" tmdbtv="288588" tmdbseason="1" tmdboffset="" tmdbid="" imdbid="">
  <name>Shinja Zero no Megami-sama to Hajimeru Isekai Kouryaku</name>
</anime>
<!-- 复杂例：<mapping-list><mapping anidbseason="0" tvdbseason="0">;1-4;</mapping></mapping-list> -->
```

### 4.4 Kometa-Team/Anime-IDs
- https://github.com/Kometa-Team/Anime-IDs ，`anime_ids.json`（以 AniDB id 为 key，1.7MB）：
```json
{"19822":{"tvdb_id":472640,"tvdb_season":1,"tvdb_epoffset":0,"mal_id":63292,"anilist_id":205896}}
```

### 4.5 bangumi-data（见 §3）
唯一同时含 **bgm.tv ↔ bangumi.moe ↔ anidb/mal/anilist/tmdb** 的数据集，是 bgm.tv 进入上述 ID 网的桥梁：`bgm → (anidb|mal) → Fribb → tvdb/tmdb/imdb`。

### 4.6 animetosho（种子 → AniDB 集）
- 文档 https://animetosho.org/feed ；实测 `https://feed.animetosho.org/json` 当天 TLS 失败，**`https://animetosho.org/feed/json` 可用**。参数：`q=`、`aid=`、`eid=`、`show=torrent&id=`（详情含每文件 crc32/md5/sha1/ed2k/字幕轨）。
- 只收录 nyaa/TT/AniDex/nekoBT 等，**不含 bangumi.moe**，但大量 raw/英字发布与 bangumi.moe 同 infohash（如 Kirara Fantasia/ANi 多站同发）。
- ⚠ 当天 `/feed/json` 最新条目 timestamp=2026-05-08，`aid=19822` 返回 `[]` → 疑似停更，只能用作历史数据。
```json
{"id":764689,"title":"Lupin III - The Legend of the Gold of Babylon (1985) (BDRip 1920x1080p x265 HEVC OPUS 2.0x2)(Dual Audio)[MICO XD]",
 "nyaa_id":2107640,"info_hash":"ad2464124afbac78eb47d872c9090759c5d2123e","total_size":7680159471,"num_files":1,
 "anidb_aid":989,"anidb_eid":10164,"anidb_fid":null,"timestamp":1778284611}
```

### 4.7 现成的 "种子 → bgm subject" 聚合
- **AnimeGarden**（https://github.com/yjl9903/AnimeGarden ，1.3k★，活跃）：抓 dmhy/moe/mikan/ANi，资源带 bgm `subjectId`，开放 API：
  `GET https://api.animes.garden/resources?subject=621073&pageSize=50` → 7 条（dmhy+moe）
```json
{"id":2468326,"provider":"moe","providerId":"6ad38fb7992853e4c9849855",
 "title":"[黒ネズミたち] 和沒有信徒的女神大人一起攻略異世界 / Shinja Zero no Megami-sama - 01 (ABEMA 1920x1080 AVC AAC MKV)",
 "magnet":"magnet:?xt=urn:btih:4c092aace051b607825bfa6a86735ecf4c40448c","size":783044608,
 "createdAt":"2026-10-17T15:09:43.826Z","fansub":{"id":612,"name":"Kirara Fantasia"},"subjectId":621073}
```
  只到 subject 级，无集号；可作交叉校验或直接复用其解析器。
- **Mikan Project**（mikanani.me）：剧集页 URL 以 infohash 为 key（`/Home/Episode/4c092aace051b607825bfa6a86735ecf4c40448c`），番组页链接 bgm.tv；RSS `https://mikanani.me/RSS/Search?searchstr=…`。
- AutoBangumi（https://github.com/EstrellaXD/Auto_Bangumi ，8.3k★）：中文字幕组命名解析规则可参考。

### 4.8 Sonarr 的需求
- Sonarr 以 **TVDB** 为系列主键（v4 起 Skyhook 后端），动画系列类型使用**绝对集号**；标题匹配依赖 TVDB 别名 + scene mapping（`services.sonarr.tv` 的 XEM/scene 名，TheXEM 映射季↔绝对集）。
- 通过 **Torznab** 索引器（常经 Prowlarr/Jackett）获取资源；搜索参数 `t=tvsearch&tvdbid=&season=&ep=` 或 `q=`。返回 item 可带 `torznab:attr` `tvdbid`、`infohash`、`magneturl`、`size`、`seeders`。
- Sonarr 自己解析标题：期望 `[Group] Title - 01 [1080p]` 式，中文标题/“第01话”/`[01]` 支持差 → 我们的 Torznab 输出可**重写 title 为 Sonarr 友好的英文/罗马音名 + 绝对集号**，并附 tvdbid，由映射库保证正确。

### 4.9 发布名解析器
| 项目 | 语言 | 备注 |
|---|---|---|
| erengy/anitomy | C++ | 原版，2026-07 仍有提交 |
| nssteinbrenner/anitogo | Go | anitomy 移植 |
| Rapptz/anitomy-rs、tylergibbs2/anitomy-ng、Xtansia/rust-anitomy | Rust | anitomy-ng 2026-10 活跃 |
| yjl9903/anitomy | TypeScript | AnimeGarden 作者，2026-10 活跃，适合 Node 后端 |
| igorcmoura/anitopy | Python | |
| sugiruu/anitomyscript、anitomy-js | JS/WASM | |
anitomy 对 `[ANi] 中文名 - 01 [1080P][Baha]…`、`【喵萌奶茶屋】★10月新番★[xxx][01][1080p][简日双语]`、`第01話`、`01v2`、`01-12`（合集）需额外预处理；bangumi.moe 的 `/api/tag/suggest` 可补充 team/分辨率/格式。

---

## 5. 弹幕来源

### 5.1 dandanplay 开放 API
- 文档 https://doc.dandanplay.com/open/ ，swagger：`https://api.dandanplay.net/swagger/v2/swagger.json`
- **所有接口需 `X-AppId` + `X-AppSecret`**（或签名模式），未带时实测 `/api/v2/search/anime`、`/api/v2/comment/{id}` 均 **403**。需向弹弹play 申请 AppId；Secret 不能下发到浏览器 → **必须服务端代理**。
- 关键接口：
  - `GET /api/v2/bangumi/bgmtv/{bgmtvSubjectId}`：**用 bgm.tv subject id 直接取弹弹作品及其剧集列表**（官方说明：映射人工维护，可能缺失/错误）。
  - `POST /api/v2/match` `{fileName, fileHash, fileSize, videoDuration, matchMode}`：`fileHash` = **文件前 16MB 的 MD5**。WebTorrent 可优先下载前 16MB 片段计算 → 精确匹配（`isMatched:true`）；结果可回写映射库（infohash+文件 → ddp episodeId）。
  - `GET /api/v2/comment/{episodeId}?withRelated=true&chConvert=1`：弹幕（`withRelated` 会合并 B 站/巴哈等第三方源）。
  - `/api/v2/search/episodes`、`/api/v2/search/anime`、`/api/v2/bangumi/{animeId}`。
- 映射价值：有了 `torrent file → bgm episode(sort)`，即可 `bgm subject → ddp anime → 第 n 集 episodeId → 弹幕`，无需依赖文件 hash。

### 5.2 bilibili
- bangumi-data 中有 `site:"bilibili"`（media/season id）；B 站弹幕接口（`/x/v1/dm/list.so?oid=<cid>`、protobuf `/x/v2/dm/web/seg.so`）需 season→ep→cid 两跳，且非公开稳定 API、有风控/版权地区限制。建议优先走 dandanplay 的 `withRelated`，不直接抓 B 站。

---

## 6. 推荐架构：独立 "元数据/映射服务"（暂名 `anime-map`）

### 6.1 组件
```
           ┌─────────── ingest workers (cron) ───────────┐
bangumi-data(daily) ─┐                                    │
Fribb/Anime-Lists(weekly)─┤→ ID graph                     │
bgm.tv API / dump ───┘   (works, episodes, external_ids)  │
bangumi.moe /api/v2/torrent/page (每 5min 增量) ─→ torrents → matcher → torrent_episode_map
AnimeGarden / Mikan (可选校验)                             │
dandanplay (按需, 服务端) ────────────────────────────────┘
              ↓
   PostgreSQL ── public REST API / Torznab ── admin UI(人工修正)
              ↓
   projectk.org 前端（时间线、播放页、弹幕）
```

### 6.2 表结构（PostgreSQL）
```sql
work(id PK, bgm_id UNIQUE, type, title_ja, title_cn, title_en, cover_url,
     begin_at timestamptz, broadcast_rrule text, total_eps, season_of int, updated_at)
work_alias(work_id, alias, lang, source)                    -- 匹配用别名（bgm/bangumi-data/moe tag synonyms）
external_id(work_id, provider, ext_id, season, ep_offset, source, confidence,
            PRIMARY KEY(provider, ext_id, season))          -- provider: anidb/mal/anilist/tmdb/tvdb/imdb/bangumi_moe/dandanplay/gamer/bilibili
episode(id PK, work_id, bgm_ep_id UNIQUE, type, sort numeric, ep numeric,
        airdate date, air_at timestamptz, title, title_cn, duration)
episode_external(episode_id, provider, ext_id)              -- anidb eid / tvdb (S,E) / absolute / ddp episodeId
torrent(infohash PK, source, source_id, title, team, size_bytes, publish_at,
        magnet, tags jsonb, raw jsonb)
torrent_file(infohash, idx, path, size_bytes, md5_16m NULL, PRIMARY KEY(infohash, idx))
torrent_episode_map(infohash, file_idx NULL, episode_id, work_id,
        resolution, lang_tags, method, confidence real,
        status ENUM('auto','verified','rejected'), by_user, updated_at,
        PRIMARY KEY(infohash, coalesce(file_idx,-1), episode_id))   -- 合集一种对多集
match_rule(id, team, pattern_regex, work_id, ep_offset, note)       -- 管理员针对字幕组/作品写的修正规则
audit_log(id, actor, entity, entity_id, before jsonb, after jsonb, at)
```

### 6.3 自动匹配流水线（bangumi.moe → bgm episode）
1. **作品级（subject）**，按置信度依次：
   a. 种子 `tag_ids` 中 `type=bangumi` 的 tag → `external_id(provider='bangumi_moe')` → work（来自 bangumi-data，**主路径**，conf 0.95）；
   b. `introduction` 中的 `bgm.tv/subject/\d+`（0.95）或 anilist/mal/tmdb 链接经 ID 图反查（0.9）；
   c. AnimeGarden 同 infohash/providerId 的 `subjectId`（0.85）；
   d. 发布名解析出标题 → `work_alias` 模糊匹配（pg_trgm），限定"本季 + 上季"候选（0.6~0.8）。
2. **集级**：anitomy 系解析 `title` 与 `content[]` 每个文件名 → episode_number / version / 范围；考虑 `match_rule.ep_offset`（续作季绝对集号 vs bgm `sort`）；类型（SP/OVA/OP）映射 `episode.type`。合集按文件逐一映射（`file_idx`）。
3. **校验**：发布时间应 ≥ 该集 `air_at - 2h`（防集号误判）；同字幕组前后集连续性；冲突时降置信度。
4. `confidence < 0.8` 或冲突 → 进入管理员审核队列；管理员确认/修改写 `status='verified'`，可一键生成 `match_rule`（如"某字幕组该作品集号 -12"），后续自动套用；所有修改进 `audit_log`。
5. 可选：浏览器播放时上报 `md5_16m`（前 16MB MD5），服务端调用 ddp `match` 回填 `episode_external(provider='dandanplay')`，同时作为集级交叉验证。

### 6.4 时间线生成
- 本季作品 = bangumi-data `begin` 在 [季初-3 月, 季末] 且 `end` 为空或未来；按 `broadcast` 展开每集 `air_at`（可选按地区选 `sites[gamer|bilibili].broadcast`），用 bgm `episodes.airdate` 修正，并 join `torrent_episode_map` 标注"已有资源(数量/最佳清晰度)"。
- 首页 API 一次返回 7 天窗口，按 `air_at` 排序（类似动画疯首页）。

### 6.5 公开 API 形态（REST + Torznab）
```
GET /v1/schedule?from=2026-10-12&to=2026-10-19&tz=Asia/Shanghai
    → [{air_at, work:{id,bgm_id,title_cn,cover}, episode:{id,sort,title_cn}, torrents_count, best:{infohash,resolution,team}}]
GET /v1/works/{id}                        (含 external_ids 全量)
GET /v1/works/lookup?provider=tvdb&id=472640[&season=1]
GET /v1/works/{id}/episodes
GET /v1/episodes/{id}/torrents?resolution=1080p&lang=chs
GET /v1/torrents/{infohash}               → {files:[{idx,path,size,episode:{bgm_ep_id,sort,anidb_eid,tvdb:{s,e},absolute}}], work, confidence, status}
GET /v1/episodes/{id}/danmaku             (服务端代理 dandanplay，带缓存)
POST /v1/admin/mappings  /  PATCH ...     (鉴权，人工修正)
GET /torznab/api?t=caps|search|tvsearch&q=&tvdbid=&season=&ep=&apikey=
    → Sonarr/Prowlarr 可直接添加；item 带 tvdbid/infohash/magneturl，title 重写为
      "[Team] <TVDB 英文名> - <absolute ep> [1080p]" 以便 Sonarr 解析
```
- 导出：每日发布全量映射 dump（JSONL：`infohash, file_idx, bgm_subject, bgm_ep, anidb_aid, anidb_eid, tvdb_id, tvdb_season, tvdb_ep, absolute`），方便第三方/社区复用。

### 6.6 运维与礼貌
- bangumi.moe：增量拉 `/api/v2/torrent/page/1..k` 直到遇到已知 `_id`，间隔 ≥5 分钟；tag 用 `/api/tag/fetch` 批量 + 缓存。
- bgm.tv：带合规 UA，单并发、指数退避；冷启动用 Archive dump；当天出现大量 52x，**不要在用户请求路径上实时调用**。
- bangumi-data / Fribb / Anime-Lists：每日 / 每周拉取 jsDelivr / raw.githubusercontent。
- dandanplay：AppSecret 只放服务端；弹幕按 episodeId 缓存（如 1h）。
- 许可：manami/Fribb 为 ODbL（再分发需署名、同许可）；bangumi-data MIT；bgm.tv 数据注意其使用条款。

### 6.7 风险
- **WebTorrent 浏览器端只能连 WebRTC peer**，bangumi.moe 种子几乎全是传统 BT（TCP/uTP）用户 → 需要服务端"桥接种子"（webtorrent-hybrid / 服务端下载转 WebSeed/HTTP 流），这也是旧代码 `api/v1/torrents/add` 的用途，重建时需保留。
- 续作季集号（绝对 vs 季内）、总集篇/0.5 集、合集包、v2 重发 → 需 `match_rule` + 人工审核。
- bangumi-data 的 bangumi_moe 覆盖率约 82%，缺失项需管理员在后台按 tag 绑定。
