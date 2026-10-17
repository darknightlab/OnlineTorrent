# qBittorrent 5.3 / libtorrent 2.1 WebTorrent 支持调研

调研日期：2026-10-17。只读调研，所有仓库都没有改动。测试服务器上没有添加或删除种子，也没有修改任何设置。

## TL;DR

- WebTorrent（WebRTC）支持来自 **libtorrent 2.1**，qBittorrent 这边基本只是把它暴露出来。qBittorrent 5.3 只要链接的是用 `webtorrent=ON` 编译的 libtorrent ≥ 2.1，就会作为真正的 WebRTC peer 向 `ws://` / `wss://` tracker 宣告。它会生成 offer，也会应答浏览器发来的 offer，浏览器可以直接从它下载。
- qBittorrent 5.3 唯一新增的相关设置是 `webtorrent_stun_server`（WebAPI 2.16.1）。其余 WebRTC 参数都没有暴露。
- 测试服 `v5.3.0beta1` / libtorrent `2.1.1.0` / WebAPI `2.16.2` 实测可用：某个种子的 `wss://tracker.projectk.org` 显示 status=2（Working）。
- **坑 1（重要，已核实）**：从 5.3.0rc1 起（PR #24512，2026-09-06 合入 master，提交 `6d2dcf4d44`，rc1 Changelog 记为 BUGFIX），qBittorrent 在 `TorrentDescriptor::parse()`（即**磁力链接 / 裸 infohash** 解析）里**无条件**只保留 `http` / `https` / `udp` tracker，`ws://` / `wss://` 会被丢掉。这里没有 `LIBTORRENT_VERSION_NUM` 判断，也没有 `TORRENT_USE_RTC` 或编译开关，即使 libtorrent 2.1 开了 webtorrent 也一样。master、v5_3_x、release-5.3.0rc1 三处代码一致；5.3.0beta1 还没有这段代码。`.torrent` 文件（`load()` / `loadFromFile()`）不经过这个过滤。规避方法：用全局"自动添加 tracker"（`add_trackers`，在过滤之后才追加到 `add_torrent_params`），或者添加后再调 `torrents/addTrackers`。这两条路径都不做协议过滤。详见 2.2。
- **坑 2**：有些官方安装包编译时关掉了 webtorrent，例如 5.3.0rc1 的 Windows 安装包（issue #25058）。`lt12` 构建（libtorrent 1.2）肯定不支持。部署时必须确认 `buildInfo.libtorrent` ≥ 2.1，并且 wss tracker 不报 "unsupported URL protocol"。
- **坑 3**：v2-only 种子在浏览器端不可用，因为 webtorrent JS 只认 v1 infohash。hybrid 种子可以用，浏览器走的是 v1 hash。
- 浏览器端：`webtorrent@3.0.21`（纯 ESM，有 dist bundle）。用 Service Worker 加 `createServer({controller})`，再用 `file.streamTo(video)` 或 `file.streamURL` 实现流式播放和拖动。
- 后端也可以自己当 WebRTC 种子：可以用 Node `webtorrent`（≥2.3 原生支持，`webtorrent-hybrid` 已废弃），或者 Go 的 `anacrolix/torrent`（基于 pion）。Rust 的 rqbit 目前不支持。

---

## 1. qBittorrent 5.3 的 WebTorrent 支持

### 1.1 版本关系
| 组件 | 状态 |
|---|---|
| libtorrent 2.1.0 | 2026-07-09 发布。ChangeLog 写有 "added support for WebTorrent"，并且 "removed support for BEP 17 web seeds (in favor of BEP 19)"。https://github.com/arvidn/libtorrent/blob/RC_2_1/ChangeLog |
| libtorrent 2.1.1 / 2.1.2 | 2.1.1 修了 "require webtorrent RTC offer IDs to be exactly 20 bytes"，2.1.2 于 2026-09-25 发布。2.1.3 还没发布，会包含更多 websocket tracker 修复（#8971、#8979 已合入 RC_2_1；#8958 "Recover stalled WebTorrent tracker announces" 还没合）。|
| qBittorrent 5.3.0beta1 | 2026-09-05。测试服跑的就是这个版本（libtorrent 2.1.1.0）。|
| qBittorrent 5.3.0rc1 | 2026-09-28，WebAPI 仍然是 2.16.2。稳定版 5.2.4 是 WebAPI 2.15.1。|

- qBittorrent 侧的适配：
  - PR #23953 "Support build against latest version of libtorrent"：让 qBittorrent 能编译通过 libtorrent 2.1，能识别 WebRTC 连接（peer 列表里 `connection` = `"WebRTC"`），并加入了 `pread_disk_io`。https://github.com/qbittorrent/qBittorrent/pull/23953
  - PR #24564：GHA CI 构建开启 `-Dwebtorrent=ON`。https://github.com/qbittorrent/qBittorrent/pull/24564
  - PR #24837 "Expose libtorrent setting for WebTorrent STUN server"：新增 `webtorrent_stun_server`，默认值 `stun.l.google.com:19302`，配置键 `BitTorrent/Session/WebTorrentSTUNServer`。只有 `LIBTORRENT_VERSION_NUM >= 20100` 时才生效；libtorrent 低于 2.1 时，WebUI 里这一行会被隐藏。https://github.com/qbittorrent/qBittorrent/pull/24837
  - 5.3 Changelog：https://github.com/qbittorrent/qBittorrent/blob/master/Changelog
- PR #24613 曾想默认给所有种子追加 wss tracker，最终被关闭，没有合入，webtorrent 开发者也明确反对。所以 qBittorrent **不会自动加 wss tracker**，需要自己配 `add_trackers`。https://github.com/qbittorrent/qBittorrent/pull/24613

### 1.2 工作方式（libtorrent 2.1 源码）
- tracker 层支持 `ws` / `wss` 协议（`src/tracker_manager.cpp`），走 `websocket_tracker_connection`。它和 tracker 保持一条长连接 WebSocket，按 WebTorrent 信令协议收发 JSON：
  - announce 时带 `info_hash`、`numwant`、`offers[]`（SDP）；
  - 收到浏览器的 `offer` 后回 `answer`。
  - info_hash 必须是 20 字节。
  - 来源：https://github.com/arvidn/libtorrent/blob/RC_2_1/src/websocket_tracker_connection.cpp
- WebRTC 实现是内置的 **libdatachannel** 子模块（静态链接，`NO_WEBSOCKET` / `NO_MEDIA`）。它用 `rtc_stream` 包成普通 peer socket，`socket_type_t::rtc` 就是 qBittorrent 里显示的 `WebRTC`。
- 所以 qBittorrent 是**双向的完整 WebRTC peer**：既能给浏览器做种，也能从浏览器或其他 libtorrent 2.1 客户端下载。
- 对 hybrid 种子，announce 会按 v1 / v2 两种协议版本分别进行（`announce_with_tracker` 按 `protocol_version` 循环），两种都会发。浏览器用 v1 hash 匹配，所以 **hybrid 可以用**；**v2-only 种子浏览器端不可用**，webtorrent JS 不支持 v2。

### 1.3 编译开关
- libtorrent **2.1.0 正式版起默认开启**：CMake 选项 `-Dwebtorrent=ON|OFF`，b2 选项 `webtorrent=on|off`，宏 `TORRENT_USE_RTC`。注意 2.1.0-rc2 时默认还是 OFF。
  - https://github.com/arvidn/libtorrent/blob/RC_2_1/docs/upgrade_to_2.1.rst
  - https://github.com/arvidn/libtorrent/blob/RC_2_1/CMakeLists.txt
- 从源码编译时要 `git submodule update --init`（`deps/libdatachannel`）。libdatachannel 可以选择用 GnuTLS 或 OpenSSL。
- 打包方关掉这个开关的实例：5.3.0rc1 的 SourceForge Windows 安装包就是 `webtorrent=OFF`，维护者已承认会在下个版本打开。https://github.com/qbittorrent/qBittorrent/issues/25058
- 判断方法：
  - `GET /api/v2/app/buildInfo` 返回的 `libtorrent` 应 ≥ `2.1`；
  - 给种子加一个 wss tracker，如果 tracker msg 是 "unsupported URL protocol"，说明没编进去；
  - `app/preferences` 里有 `webtorrent_stun_server` 键，只能说明 qBittorrent 是对着 lt 2.1 编译的，**不能证明** RTC 被编进去了。
- Docker 镜像（linuxserver、qbittorrent-nox-static 等）需要逐个确认 libtorrent 版本和编译选项。`*_lt12_*` 资产肯定不支持。

### 1.4 相关设置
libtorrent `settings_pack` 里的相关项（https://github.com/arvidn/libtorrent/blob/RC_2_1/include/libtorrent/settings_pack.hpp ）：

| libtorrent 设置 | 默认值 | qBittorrent 是否暴露 |
|---|---|---|
| `webtorrent_stun_server` | `stun.l.google.com:19302`（格式 `host:port`，**只能填一个**） | 是，`webtorrent_stun_server` |
| `max_webtorrent_offers` | 10（每次 announce 生成的 offer 数，同时也是每个种子并发入站协商的上限；设为 0 表示关闭） | 否 |
| `webtorrent_connection_timeout` | 120s | 否 |
| `min_websocket_announce_interval` | 60s | 否 |
| `validate_https_tracker_certificate` | 同时作用于 wss tracker | 是（`validate_https_tracker_certificate`） |

- **TURN**：没有专门的配置项。`rtc_signaling.cpp` 只是把这一个字符串塞进 libdatachannel 的 `iceServers`。理论上 libdatachannel 能解析 `turn:user:pass@host:port` 形式的 URL，但这是未公开、未验证的用法。服务器有公网 IP 时只用 STUN 就够了，浏览器侧可以自己配 TURN。
- tracker 宣告策略：`announce_to_all_trackers` / `announce_to_all_tiers` 会影响 wss tracker 是否被宣告到。如果 wss tracker 和普通 tracker 在同一 tier，又没开 all_trackers，可能只宣告第一个可用的。测试服两项都是 true，没问题。
- 已知问题：长时间做种后 WebTorrent 停止响应，tracker 一直卡在 "Updating"。https://github.com/qbittorrent/qBittorrent/issues/24899 ，相关修复见 libtorrent #8958（未合）、#8688（已合入 RC_2_1，2.1.1 中应已包含）。**记录在案，暂不做对策（待实际环境观察）**。

### 1.5 BEP19 webseed / HTTP 服务
- qBittorrent / libtorrent **不提供** BEP19 webseed 服务端，只作为客户端消费 `url-list`。libtorrent 2.1 删掉了 BEP17（httpseeds）。
- 新增的 `GET/POST /api/v2/torrents/downloadFile?hash=&file=`（WebAPI 2.16.0，PR #24135）：
  - 可以下载**已完成**的文件，`file` 参数可以是索引，也可以是相对路径；
  - 新的 HTTP 层**支持 Range**；
  - 但需要 WebUI 认证，不能直接给终端用户的浏览器用。可以考虑由后端反代，做成 HTTP 回退或 webseed（`ws=`）。
  - https://github.com/qbittorrent/qBittorrent/pull/24135
  - 另一条路是直接用 nginx 暴露 save_path 作为 webseed。webseed URL 规则：多文件种子是 `base/<torrent name>/<path>`。

## 2. WebAPI 5.x 变更（与本项目相关）

来源：https://github.com/qbittorrent/qBittorrent/blob/master/WebAPI_Changelog.md ，以及 rc1 源码 `src/webui/api/*.cpp`。WebAPI 版本对应：5.0 是 2.11.0，5.2.x 是 2.15.x，5.3 beta1/rc1 是 2.16.2，master 是 2.17.0。

### 2.1 Stop / Pause 改名（5.0 / 2.11.0 起）
- 接口：`torrents/pause` 改为 `torrents/stop`，`torrents/resume` 改为 `torrents/start`。
- 状态：`pausedUP` / `pausedDL` 改为 **`stoppedUP` / `stoppedDL`**。
- `filter` 参数：`paused` / `resumed` 改为 `stopped` / `running`。
- `torrents/add` 的 `paused` 参数改为 **`stopped`**。
- 偏好 `add_stopped_enabled`（测试服为 false）。
- rc1 完整状态集：`error missingFiles uploading stoppedUP queuedUP stalledUP checkingUP forcedUP downloading metaDL forcedMetaDL stoppedDL queuedDL stalledDL checkingDL forcedDL checkingResumeData moving unknown`
- 测试服现状：stalledUP 48、stoppedUP 33、uploading 11，共 92 个种子。

### 2.2 `torrents/add`（rc1 实际接受的参数）
`urls`、torrents 文件、`savepath`、`downloadPath`、`useDownloadPath`、`category`、`tags`、`rename`、`stopped`、`stopCondition`、`addToTopOfQueue`、`autoTMM`、`contentLayout`、`sequentialDownload`、`firstLastPiecePrio`、`forced`、`seedMode`、`upLimit`、`dlLimit`、`ratioLimit`、`seedingTimeLimit`、`inactiveSeedingTimeLimit`、`shareLimitsMode`、`shareLimitAction`、`filePriorities`、`downloader`

- **2.16.0：删除 `skip_checking`，改用 `seedMode`**（PR #24724）。旧代码要改。
- 2.14.0 起返回 JSON：`{success_count, pending_count, failure_count, added_torrent_ids}`。
  - 有 pending（例如需要先拉元数据的 URL）时返回 202，全部失败时返回 409。
  - `added_torrent_ids` 对 v1 / hybrid 种子就是 v1 hash，**可以直接拿它关联记录**。
- 没有直接传 trackers 的参数。另外，**从 rc1 起磁力里的 `ws://` / `wss://` 会被过滤**（已对照源码核实）：
  - 来源：PR #24512 "Filter unsupported tracker URLs from magnets"（作者 TurboTheTurtle，目标分支 master，2026-09-06 合入，合并提交 `6d2dcf4d44`，Closes #23937）。它最初是为了挡掉磁力 `tr=` 里混进来的 HTML 等垃圾值。https://github.com/qbittorrent/qBittorrent/pull/24512
  - 代码（`src/base/bittorrent/torrentdescriptor.cpp`，release-5.3.0rc1 第 93–173 行，master / v5_3_x 与之相同）：
    ```cpp
    bool isSupportedTrackerURL(const QString &url)
    {
        const QUrl trackerURL {url};
        if (!trackerURL.isValid() || trackerURL.host().isEmpty())
            return false;
        const QString scheme = trackerURL.scheme();
        return ((scheme == u"http") || (scheme == u"https") || (scheme == u"udp"));
    }
    ...
    lt::add_torrent_params params = lt::parse_magnet_uri(magnetURI.toStdString());
    removeUnsupportedTrackers(params);
    ```
    https://github.com/qbittorrent/qBittorrent/blob/release-5.3.0rc1/src/base/bittorrent/torrentdescriptor.cpp
  - **过滤没有任何条件**：不看 `LIBTORRENT_VERSION_NUM`，也不看 `TORRENT_USE_RTC`。对照来看，libtorrent 自己的 `is_valid_tracker_url`（v2.1.0 / RC_2_1 `src/parse_url.cpp`）在 `#if TORRENT_USE_RTC` 下是**放行** `ws://` / `wss://` 的；RC_2_0 的版本（arvidn/libtorrent#8202）只认 http/https/udp。qBittorrent 这边照搬了 2.0 的白名单，所以即使是 lt 2.1 + webtorrent=ON 的构建，wss 也会被它自己去掉。https://github.com/arvidn/libtorrent/blob/v2.1.0/src/parse_url.cpp
  - 影响范围：WebAPI `torrents/add` 的 `urls`（磁力 / 裸 hash）、`torrents/fetchMetadata`，以及 GUI 添加磁力，都会经过 `AddTorrentManager::addTorrent` 或 `TorrentDescriptor::parse()`。上传 `.torrent` 文件**不受影响**。磁力拿到元数据后也不会把 wss 补回来，因为 tracker 不在 info 字典里。
  - 版本：5.3.0beta1 没有这段代码，rc1 有，master 至今（2026-10-17）也没有修正。目前没查到针对 wss 被过滤的 issue 或 PR；更早的 PR #23939（白名单里含 `wss`）因长期无活动被自动关闭，没有合入。
  - 规避：`add_trackers`（`SessionImpl` 在解析之后才把 `m_additionalTrackerEntries` 追加进 `p.trackers`，`parseTrackerEntries` 不检查协议），或者添加后调 `torrents/addTrackers`（`TorrentImpl::addTrackers` 只去掉空 URL）。
- 2.11.9：`torrents/fetchMetadata` / `parseMetadata` / `saveMetadata`，可以先解析元数据（文件列表）再添加。

### 2.3 `torrents/info`
测试服实测字段：`added_on amount_left auto_tmm availability category comment completed completion_on connections_count connections_limit content_path created_by creation_date dl_limit dlspeed download_path downloaded downloaded_session eta f_l_piece_prio force_start has_metadata hash inactive_seeding_time_limit infohash_v1 infohash_v2 last_activity magnet_uri max_inactive_seeding_time max_ratio max_seeding_time name num_complete num_incomplete num_leechs num_seeds piece_size pieces_have pieces_num popularity priority private progress ratio ratio_limit reannounce root_path save_path seeding_time seeding_time_limit seen_complete seq_dl share_limit_action share_limits_mode size state super_seeding tags time_active total_size total_wasted tracker trackers_count up_limit uploaded uploaded_session upspeed`

- 2.11.8 起支持 `includeFiles=true`，结果里会内嵌 `files`，可以省掉多次 `torrents/files` 调用。
- 文件列表还可以用 `torrents/files`。2.15.1 新增 `torrents/pieceAvailability`。
- `sync/torrentPeers` 的 `connection` 字段可能是 `"WebRTC"`，可用来统计浏览器 peer。
- 2.13.0：`torrents/trackers` 新增 `endpoints[]`（含 `bt_version`）、`next_announce`；status 5 表示 tracker error，6 表示 unreachable。
- 2.11.7：新增 `has_tracker_warning` / `has_tracker_error`，只在 sync 里有。

### 2.4 分享限制
- 2.12.0：`torrents/setShareLimits` **必填** `shareLimitAction`，取值 `Default|Stop|Remove|RemoveWithContent|EnableSuperSeeding`。
- 2.15.3：**必填** `shareLimitsMode`，取值 `Default|MatchAny|MatchAll`；全局偏好 `share_limits_mode`。
- 取值约定：`-2` 表示用全局，`-1` 表示不限。
- 2.11.6：`setPreferences` 的 `max_ratio_enabled` 和 `max_ratio` 等成对项只允许出现一个。

### 2.5 分类 / 标签
- 2.16.2：`createCategory` / `editCategory` 可以设 `ratioLimit`、`seedingTimeLimit`、`inactiveSeedingTimeLimit`、`shareLimitsMode`、`shareLimitAction`；`editCategory` 不再要求 `savePath`。
- **可以按分类统一给本站种子设做种策略。**
- 2.15.0 起子分类始终启用（`a/b`）。2.11.10 起 `downloadPath` 序列化为 null。
- 标签接口没变：`addTags`、`removeTags`、`createTags`、`deleteTags`。

### 2.6 认证
- `auth/login` 登录：凭证错误返回 **401**（2.14.0 起），成功返回 204。
  - Cookie 名形如 **`QBT_SID_<webui端口>`**，实测为 `QBT_SID_8888`，不要写死成 `SID`。
- **API Key**（2.14.1，PR #23212 / #23388）：
  - `app/rotateAPIKey` 生成或轮换，`app/deleteAPIKey` 删除；
  - 请求头 `Authorization: Bearer qbt_<key>`，**跳过 CSRF 检查**，但禁止访问 `auth/*`，也不能访问 WebUI 静态页；
  - 偏好里有 `web_ui_api_key` 字段，测试服为空，即未生成。
  - **后端集成推荐用 API Key**，不用维护会话和 Referer。
- **Basic Auth**（2.15.0，PR #23564）：`Authorization: Basic ...`。
- 5.3 安全收紧：
  - 用 `Sec-Fetch-Site` 判断跨站（#24792）。非浏览器的 curl 加 Referer 或 Origin 就行。
  - 修复了 `X-Forwarded-For` 伪造导致的认证绕过（#24889）。
  - `web_ui_sessions_count_limit` 会话数上限（测试服为 15）。**后端应复用会话或改用 API Key**，否则频繁 login 会顶掉旧会话。
- 其他：2.16.0 起 `search/downloadTorrent` 等只接受 POST；2.16.2 新增 `transfer/pauseSession` / `resumeSession`。

## 3. 测试服实测（https://qbittorrent-ams.kawaii.cam）

只读操作：login、version、webapiVersion、buildInfo、preferences、categories、tags、info、trackers、torrentPeers。

```
app/version        v5.3.0beta1
app/webapiVersion  2.16.2
app/buildInfo      {"bitness":64,"boost":"1.91.0","libtorrent":"2.1.1.0","openssl":"3.5.8","platform":"linux","qt":"6.11.1","zlib":"1.3.2"}
login              HTTP 204, Set-Cookie: QBT_SID_8888=...（经 Cloudflare）
```

preferences 共 235 个键。与 webtorrent、rtc、stun、webseed 相关的**只有一个**：
```
webtorrent_stun_server = "stun.l.google.com:19302"
```
其中不存在任何 `webseed`、`rtc`、`websocket` 相关的键。其他相关值：
```
add_trackers_enabled = true
add_trackers = wss://tracker.dnlab.net:16443, wss://tracker.projectk.org, wss://tracker.btorrent.xyz,
               wss://tracker.openwebtorrent.com, 之后是大量 udp/http tracker
               （空行分隔 tier；新加入的种子会自动带上这些 wss tracker）
announce_to_all_trackers = true, announce_to_all_tiers = true
validate_https_tracker_certificate = true
listen_port = 16881, upnp = false, encryption = 0, dht/pex/lsd = true
queueing_enabled = false, max_connec = 500, max_connec_per_torrent = 100, max_uploads_per_torrent = 4
max_ratio_enabled/max_seeding_time_enabled = false, share_limits_mode = "MatchAny"
auto_tmm_enabled = true, torrent_content_layout = "Subfolder", save_path = /Data/Torrent/qbittorrent/files/默认
web_ui_port = 8888, web_ui_api_key = "", web_ui_sessions_count_limit = 15, csrf/host 校验 = 开
seeding_outgoing_connections = true
```

`max_uploads_per_torrent = 4`（每个种子的上传槽位数）可能会限制同时服务的浏览器数量，可以考虑调高。

分类（name 对应 savePath；所有分类的分享限制都是 -2，即跟随全局）：
```
projectj → ""          临时 → /Data/Torrent/qbittorrent/files/临时
动画 → .../files/动画   动画/临时 → .../files/默认/动画临时   动画/手动 → .../files/动画   动画/自动 → .../files/动画
电影 → ""              电影/临时、电影/手动、电影/自动 → .../files/电影
计算机 → .../files/计算机   计算机/novelai → .../files/默认/计算机/novelai
```

标签：`[]`（空）。

wss tracker 实测：种子 `c12b852f…`（`[NanakoRaws] Watashi ga Koibito…`，v1）。

| tracker | status | 说明 |
|---|---|---|
| `wss://tracker.projectk.org` | 2（Working） | seeds=1 |
| `wss://tracker.openwebtorrent.com` | 4 | "Connection reset by peer"（偶发，见 issue #24899 的描述） |
| `wss://tracker.btorrent.xyz` | 4 | "Operation canceled" |
| `wss://tracker.dnlab.net:16443` | 4 | "Operation canceled" |

这证明测试服的 libtorrent 编译时**开启了 RTC**，并且正在向 wss tracker 宣告。其他抽查的老种子没有 wss tracker，因为它们是在设置 `add_trackers` 之前加入的，需要 `addTrackers` 补上。

## 4. 浏览器端：webtorrent npm

- 最新版 **`webtorrent@3.0.21`**（2026-07-27）。3.0.0（2026-05-24）的 breaking change 只是 Node ≥ 22，浏览器 API 基本没变。https://github.com/webtorrent/webtorrent/releases
  - 维护者：ThaUnknown、SilentBot1。WebRTC 层是 `@thaunknown/simple-peer@10`，Node 端通过 `webrtc-polyfill`（node-datachannel）实现。
- 打包与使用：
  - 包是 `"type":"module"`，靠 `browser` 字段在浏览器里屏蔽 net、dht、utp 等模块。
  - 可以 `import WebTorrent from 'webtorrent'` 后交给 bundler；官方文档写的是 webpack 和 browserify，Vite 一般能用，个别 Node 全局变量可能需要 polyfill。
  - 更省事的做法是直接 `import WebTorrent from 'webtorrent/dist/webtorrent.min.js'`（约 218KB，已打包好的 ESM）。
  - 包里同时带 `dist/sw.min.js`（Service Worker，约 1.3KB）。
- 流式播放（https://github.com/webtorrent/webtorrent/blob/master/docs/api.md ）：
  ```js
  const reg = await navigator.serviceWorker.register('/sw.min.js', { scope: '/' })
  await navigator.serviceWorker.ready
  client.createServer({ controller: reg })     // 浏览器端必需
  client.add(magnet, { announce: ['wss://tracker.projectk.org'] }, t => {
    t.files.find(f => f.name.endsWith('.mp4')).streamTo(videoEl)   // 或使用 f.streamURL
  })
  ```
  - SW 会拦截 `<scope>/webtorrent/<infoHash>/<path>` 并支持 Range，所以可以拖动。
  - 要求 HTTPS（或 localhost），并且 SW 文件要放在能覆盖目标路径的 scope 下。
  - 旧的 `renderTo` / `appendTo` 已经移除，见 issue #2911。
- ICE 配置：`new WebTorrent({ tracker: { rtcConfig: { iceServers: [...] } } })` 可以配置 STUN/TURN。浏览器侧加 TURN 能弥补 libtorrent 只能配一个 STUN 的限制。
- webseed：WebTorrent 默认 `webSeeds: true`，支持 BEP19（磁力链接的 `ws=` 或 .torrent 里的 `url-list`）。可以作为 WebRTC 连不上时的 HTTP 回退。
- 不支持 BitTorrent v2 infohash。
- 编解码和容器（以浏览器原生能力为准，webtorrent 不做转码）：
  - 文档表格：mkv 在 Chromium、Chrome、Edge 下可播，Firefox 不行；avi、mpeg 都不行；m2ts 只有 Edge 可以。这张表本身已被指出过时（issue #3008）。
  - HEVC：Chrome ≥ 107 在 macOS ≥ 11、Android 上可用；Windows / Linux 需要硬件解码（Linux 走 VAAPI）。Firefox ≥ 137 依赖硬件解码，有部分软解回退，10-bit 支持有限。来源：https://github.com/Fyrd/caniuse/blob/main/features-json/hevc.json ，https://developer.mozilla.org/en-US/docs/Web/Media/Guides/Formats/Video_codecs#hevc_h.265
  - 实际限制（经验结论，需要实测）：MKV 里的 AC3、EAC3、DTS 音轨在 Chrome 上通常没有声音；ASS/SSA 字幕不会原生渲染。
  - 建议前端用 `MediaSource.isTypeSupported`、`navigator.mediaCapabilities.decodingInfo` 做探测。不支持的格式提示用户下载，或者做 WASM 方案（ffmpeg.wasm / libav.js remux，字幕用 JASSUB 渲染），这类方案成本较高。
- 替代方案：浏览器内跑 BT 协议的成熟 JS 实现基本只有 webtorrent（以及基于它的分支）。p2p-media-loader 是 HLS/DASH 的 P2P CDN 方案，不兼容种子，只在改成 HLS 分发时才值得考虑。

## 5. 后端自己当 WebRTC 种子（简述）

| 方案 | 状态 |
|---|---|
| Node `webtorrent` ≥ 2.3 | 原生 WebRTC（node-datachannel），可以同时连接 TCP/uTP/DHT peer 和 WebRTC peer。**`webtorrent-hybrid` 已废弃**（npm deprecate 说明："deprecated as of WebTorrent >= 2.3.0 as native WebRTC support has been added"）。适合写一个轻量的"桥接做种"服务，读 qBittorrent 已完成的文件，用 `client.seed` 或 `add` 指向同一个 save_path。|
| Go `anacrolix/torrent` | README 列出支持 WebTorrent（`webtorrent/` 包，基于 pion/webrtc v4），也支持 v2 和 webseed，最新 v9.0.1（2026-08）。可以做独立的 WebRTC 做种节点或流媒体网关。https://github.com/anacrolix/torrent |
| Rust `rqbit`（librqbit） | 不支持 WebRTC，相关 issue 都还开着（ikatson/rqbit#12、#71、#622）。其他 Rust 方案也不成熟。|
| libtorrent 2.1 本身 | 就是 qBittorrent 用的那套，也可以用 python-libtorrent 2.1 写一个最小做种器。|

结论：首选 qBittorrent 5.3 原生 WebRTC，运维成本最低。真正需要的额外工作有三项：
1. 确认构建开启了 RTC；
2. 用 `add_trackers` 或 `addTrackers` 保证每个种子都带 wss tracker。这一步是**必需的**：5.3.0rc1 起 PR #24512 会在磁力解析时无条件去掉 `ws://` / `wss://`（见 2.2），与 libtorrent 是否开启 webtorrent 无关，所以不能指望磁力里自带的 wss tracker；
3. issue #24899（长时间做种后 WebTorrent 停止）：记录在案，暂不做对策（待实际环境观察）。

如果遇到 NAT 问题，或者实际观察到稳定性问题，再加一个 Node webtorrent 或 Go anacrolix 的 WebRTC 做种节点，或者提供 BEP19 webseed 作为兜底。
