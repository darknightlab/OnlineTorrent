# WebTorrent / 浏览器 P2P 直播调研

调研日期：2026-10-19。只读调研，生产代码没有改动。原型放在 `/tmp/live-lab`（见第 4 节）。

## TL;DR

- **BitTorrent 协议本身不支持"直播"**。infohash 是对 info 字典（包括每个 piece 的哈希和总长度）做的 SHA-1，内容只要追加一点，就会变成另一个种子、另一个 swarm。BEP 39 / 44 / 46 都只能做到"把订阅者指向一个新的 infohash"，并且至今都是 Draft。浏览器里的 WebTorrent 不带 DHT（`"bittorrent-dht": false`），所以 BEP 44/46 在浏览器里根本用不了。
- 浏览器 P2P 直播目前事实上的方案是 **HLS/DASH 分片 + WebRTC 互传 + WebTorrent tracker 做信令**，开源实现里最活跃的是 **Novage p2p-media-loader**（Apache-2.0，v4.0.0 于 2026-08-19 发布，v5 alpha 已出，周下载约 1.4k）。PeerTube 直播用的就是它，client 依赖 `p2p-media-loader-*@^4.0.0`。
- **延迟和 P2P 分流比是此消彼长的**。PeerTube 1000 浏览器压测：直播"普通延迟"（约 30 s）时 P2P 节省 75% 带宽，VOD 节省 98%。PeerTube 的"低延迟"模式直接关掉 P2P，理由是分片太短，分享不起来。
- **本地原型实测**（wss://tracker.projectk.org + p2p-media-loader v4 + hls.js 1.7.3，2 个观众，2 s 分片）：
  - 延迟约 5 s 时 P2P 字节占比只有 **6%**；
  - 约 11 s 时 **46%**（2 个观众的理论上限是 50%）；
  - 两人分别落后 6 s / 28 s 时 **37%**；
  - 全程 0 卡顿。我们现有的 wss tracker 不用改就能给它做信令。
- **建议**：
  1. 先做 **"同步观看房间 / 首映"**：复用现有 WebTorrent VOD，只加一个房间时钟同步层。工作量 3–5 天，没有转码成本，大家看同一位置，swarm 效果最好。
  2. 确实需要 24/7 频道时，再上 **ffmpeg 播出 → HLS → hls.js + p2p-media-loader + ArtPlayer**，目标延迟 20–30 s。
- 不建议做的方向：每个分片一个 torrent、单个持续增长的 torrent、让 qB/libtorrent 参与直播 swarm。

---

## 1. 标准和扩展：有没有"直播 / 可变 torrent"

| 方案 | 内容 | 状态 | 对直播的意义 |
|---|---|---|---|
| **BEP 39** Updating Torrents Via Feed URL | info 字典里放 `update-url`，客户端带 `info_hash` 去轮询，拿到新的 .torrent（可用 BEP 35 签名） | Draft，2012-10-19，Arvid Norberg / Chris Brown | 只是"换种子"，每次更新都是新 swarm。粒度是分钟到小时，适合连载 |
| **BEP 44** DHT 存任意数据 | DHT `put/get`，有 immutable 和 mutable 两种（mutable 用 ed25519 公钥 + seq） | Draft，2014-12-19 | 只是 key/value 通道，本身不传视频 |
| **BEP 46** 通过 DHT mutable item 更新 torrent | 发布者往 BEP 44 的 mutable item 里写 `{ih: <infohash>}`，消费者只需要知道公钥 + salt | Draft，2016-07-24，Luca Matteis | 跟 BEP 39 一样只能"指向新 infohash"。浏览器没有 DHT，用不了 |
| **BitTorrent Live**（Bram Cohen） | 自研的低延迟树/网状推流协议，不是 BEP，也不兼容 BitTorrent 线协议 | 2012-02 发布（TechCrunch），2013-03 公开 beta，2016 年改名 btlive.tv，之后再无消息。协议没有公开标准化 | 已经停了，没有可用的实现 |
| **Tribler / P2P-Next**（NextShare / SwarmPlayer，2008–2011） | 改造过的 BitTorrent：infohash 由发布者公钥派生，piece 改成单独签名，加滑动窗口；后来演变成 libswift，标准化为 **IETF PPSPP RFC 7574**（2015） | 只是研究成果。现在的 Tribler（GPL-3.0）主打匿名下载和流式 VOD，没有直播 | 证明"可变 swarm"必须改协议（签名 piece + 窗口），普通客户端和 WebTorrent 都不支持 |
| **PPLive / PPStream / 早期 PPTV** | 私有协议，mesh-pull：buffer map 加 chunk 调度 | 已商业化后转向 CDN | 学术测量（Hei 等人，2007，IEEE TMM）显示节点之间的播放时差有数十秒到分钟级。现在的 HLS+P2P 本质上是同一个思路的 Web 版 |

**WebTorrent 能做什么**：webtorrent@3.0.21 的 browser 字段里 `bittorrent-dht`、`utp`、`ut_pex`、`conn-pool` 全是 `false`。浏览器端只能靠 wss tracker 加 WebRTC 发现 peer，也没有实现 BEP 39/46。一个 torrent 就是一份不可变的内容。

## 2. 现有的浏览器 P2P 直播 / 流媒体项目

数据均为 2026-10-19 从 GitHub API 和 npm registry 查询得到。

| 项目 | 许可 | 活跃度 | npm 周下载 | 说明 |
|---|---|---|---|---|
| **Novage/p2p-media-loader**（core / hlsjs / shaka） | Apache-2.0 | ★1726，当天有 push。v3.0.0（2026-06）去掉了 bittorrent-tracker 依赖，自带浏览器原生 WebTorrent 信令；v4.0.0（2026-08-19）与 v3 在线协议兼容；v5.0.0-alpha.3（2026-09） | core 1402 / hlsjs 1428 / shaka 86 | 支持 HLS/DASH 的直播和 VOD。一条流（每个码率）对应一个 swarm，用的是 WebTorrent tracker 协议，但**数据面是它自己的分片协议**。默认 tracker 是公共的，FAQ 明确说生产环境不要用公共 tracker。支持 iOS 17.1+（ManagedMediaSource） |
| Novage/wt-tracker | Apache-2.0 | ★277，活跃 | — | uWebSockets.js 写的 tracker。我们已经有 wss tracker，不需要它 |
| greatest-ape/aquatic | Apache-2.0 | ★628，2026-07 | — | p2pml 官方推荐的 tracker 之一 |
| **PeerTube**（直播） | AGPL-3.0 | ★15365，活跃 | — | 用法：client 依赖 `p2p-media-loader-hlsjs@^4.0.0`（旧 fork `@peertube/p2p-media-loader-*` 停在 2.0.1 / 2024-11）。服务端用 ffmpeg 输出 HLS，分片 4 s（低延迟模式 2 s）。直播时 `highDemandTimeWindow: 4`，hls.js `liveSyncDurationCount` 按模式取 小 2 / 默认 5 / 高 10 个分片，**小延迟模式直接禁用 P2P**。服务端维护每个分片的 sha256（`live-segment-sha-store.ts`），客户端在 `segment-validator.ts` 里校验 P2P 收到的分片 |
| live-torrent（pldubouilh） | MIT | ★178，**最后 push 2020-02** | — | "每个分片一个 torrent"的 PoC：服务端把 .ts 做成 magnet 写进 m3u8，浏览器端用 SW + WebTorrent。已停止维护 |
| Nile.js（2017） | — | 仓库已不存在 | — | WebTorrent 直播实验，已死 |
| SwarmCloud / CDNBye（hlsjs-p2p-engine 等） | GitHub 上没有 license，npm 标注 MIT | ★1072，2026-01；`@swarmcloud/hls` 2.18.0（2026-05） | 123 + cdnbye 234 | 商业服务：要在它的 dashboard 注册域名，信令和统计走它的云端。另有一个 SW 版本声称支持 iOS Safari。依赖第三方，不适合我们的私有站 |
| Peer5 | 商业 | YC W17。**2021-08 被微软收购**，2022-09 变成 Microsoft eCDN，只服务 Teams/Stream | — | 不再对外提供 |
| Streamroot | 商业 | **2019-09-10 被 CenturyLink（Lumen）收购**，改名 Mesh Delivery。Lumen 2023-10 把 CDN 合同卖给 Akamai，CDN 业务收缩 | — | 未见独立产品继续提供 |
| Livepeer（go-livepeer） | MIT | ★586，活跃 | — | 去中心化**转码**网络（链上激励），不是观众之间的 P2P 分发，和我们的需求无关 |
| hls.js | Apache-2.0 | 1.7.3（2026-09） | 604 万 | 支持 LL-HLS 和 ManagedMediaSource。ArtPlayer 通过 `customType.m3u8` 接入 |

### 公开的 P2P 分流数据

- **PeerTube 压测（2023-12，1000 个真实 Chrome，650 kbit/s 单码率直播）**：
  - 普通延迟（约 30 s）：5 分钟后 HTTP 90 Mbit/s，P2P 350 Mbit/s，**节省 75%**。官方说法是"降低到 1/3–1/4"，与站长们的反馈一致。
  - 高延迟（约 60 s）：HTTP 65 / P2P 370 Mbit/s，节省约 85%。
  - 一半观众关闭 P2P 时：HTTP 260 / P2P 190 Mbit/s。
  - VOD：HTTP 25 / P2P 1150 Mbit/s，**节省 98%**。
  - 注意：测试中 WebRTC 没有限速（只限了 HTTP 2 Mbit/s 下、300 kbit/s 上），所以属于偏乐观的条件。
  - 来源：https://joinpeertube.org/news/stress-test-2023
- **PeerTube 4.2 发布说明**：开 P2P 的直播延迟大约 30–40 s，所以才加了"关 P2P 的低延迟模式"和"更高延迟换更多 P2P 的模式"。来源：https://joinpeertube.org/news/release-4.2
- **p2pml FAQ**：N 个观众时理论上限是 (N-1)/N，例如 10 人最多 90%。至少有一个人要走 HTTP 从源站下载一次。

## 3. 我们可以选的架构

### (a) HLS（或 LL-HLS）+ p2p-media-loader，复用现有 wss tracker（**推荐**）

- 数据流：源站或 CDN 通过 HTTP 提供 m3u8 和 .ts/.m4s。每个浏览器先用 HTTP 拉临近播放点的分片，较远的分片走 P2P 从别人那里拿，同时把自己已有的分片分享出去。
- 延迟：
  - P2P 至少需要"领先的人已经拿到分片"和"落后的人还有时间等"之间的时间差，所以要比纯 HLS 多 1–3 个分片。
  - 实际取值：普通 HLS 一般 10–30 s；p2pml/PeerTube 默认约 20–40 s；想要显著分流，延迟一般至少 10 s，最好 20–30 s。
  - LL-HLS（2–5 s）和 P2P 基本互相冲突。p2pml 的代码里没有专门处理 LL-HLS 的 partial segment，PeerTube 在低延迟模式下也关掉了 P2P。
- 默认行为需要注意：如果不手动设 `liveSyncDurationCount`，p2pml-hlsjs 会把它改成 `min(播放列表分片数-1, 120 s / targetduration)`，也就是尽量往后退。原型中 15×2 s 的播放列表，自动设成了 14 个分片（28 s）。
- 分流比：几十人以上的同场直播，参照 PeerTube，大约 60–80%。2–3 人时最多 50–67%。实际中 NAT、上行带宽和移动端都会让这个数字下降。
- 和鉴权的配合：
  - m3u8 和分片是同源 HTTP，cookie 加我们现有的路由权限检查就能直接用。需要时可以用 `httpRequestSetup` 定制请求。
  - P2P 这一侧，**任何知道 swarm infohash 的人都能向 swarm 里的 peer 要分片**。因为 tracker 是开放的，p2pml 的 infohash 由 `swarmId` 派生，默认就是 manifest URL。
  - 对策：
    - 由后端在鉴权之后下发不可猜测的 `swarmId`（例如 `HMAC(server_secret, channel_id | 日期)`）；
    - 用 `validateP2PSegment` 拿服务端提供的分片 sha256 校验（PeerTube 的做法），防止有人投毒；
    - 内容本身就是站内可播放的 VOD 内容，泄露风险与现状相当。
- tracker：p2pml 只用 WebTorrent tracker 的信令（announce / offer / answer），不跑 BitTorrent 线协议。原型证实 `wss://tracker.projectk.org` 不改配置就能用。它和 WebTorrent VOD 的 swarm 是不同的 infohash，可以共存。如果 tracker 开了 infohash 白名单，需要把这些 hash 加进去。
- Safari/iOS：
  - hls.js 需要 MSE 或 ManagedMediaSource。iPhone 要 iOS 17.1+ 才行；用 MMS 时要设 `disableRemotePlayback` 或提供 AirPlay 备用源。
  - 更老的 iOS 只能回退到原生 HLS，没有 P2P，但能播。
  - ArtPlayer 的 `customType` 要先判断 `Hls.isSupported()`，不要在 iOS 上直接走原生 HLS，否则会绕过 P2P。
- 体积：p2pml-core 压缩后 110 KB，gzip 后约 28 KB；hlsjs 集成层 9 KB；再加上 hls.js。可以只在直播页懒加载。
- 复杂度：前端加一个 Engine，后端加 HLS 播出和静态分片服务。整体是中等。

### (b) 每个分片一个 torrent（WebTorrent）

- 做法：服务端把每个 2–6 s 的分片 `createTorrent`，通过 m3u8 或 SSE 推送 magnet；浏览器对每个分片都 `client.add`。live-torrent 就是这样做的。
- 问题：
  - 每个分片都要重新走一遍 tracker announce、WebRTC offer/answer、新建 RTCPeerConnection。WebTorrent 不会在不同 torrent 之间复用 wire，这些握手动辄 1–5 s，跟分片时长同一个量级。
  - 每个分片都要创建并销毁一个 torrent，OPFS store 和 SW 都会频繁抖动。
  - qB/libtorrent 也得每 2–6 s 添加一个新种子。
- 收益：只有"能复用 WebTorrent"这一点，但 p2pml 已经用同一套 tracker 信令解决了这个问题，并且一条流只用一个 swarm。**不推荐**。

### (c) 单个持续增长的 torrent + 滑动窗口（协议不支持）

- v1 的 infohash = SHA-1(bencode(info))，info 里包含 `length` 和 `pieces`（每个 piece 的 SHA-1 拼在一起）；v2 用的是固定的 `pieces root`（Merkle 根）。
- 只要追加或删除内容，infohash 就会变，客户端会认为这是另一个 swarm，旧连接、bitfield、have 消息都作废。
- 要支持直播，就得像 Tribler/PPSPP 那样改协议：用公钥当 swarm ID、每个 piece 单独签名、bitfield 改成窗口。WebTorrent、libtorrent 和 qB 都不支持。BEP 39/46 也只能做到"换一个新种子"。**不可行**。

### (d) qB/libtorrent 那一侧

- libtorrent 2.1 的 WebRTC 只说 BitTorrent 线协议，不能加入 p2pml 的 swarm（数据面不同），也不支持可变 torrent。所以 **qB 帮不了直播分发**。
- 它能做的事：
  - 在"首映 / 同步房间"方案里，继续给 WebTorrent VOD 做种，加上 HTTP WebSeed。这条路完全复用现有能力。
  - 在 24/7 频道方案里，只作为片源下载器：先下载完成再播出。不建议用顺序下载边下边播，因为缺 piece 会让 ffmpeg 卡住。
- 想要一个"服务端超级 peer"去喂 p2pml swarm，必须在 Node 里跑 p2pml 并配 WebRTC 实现，收益不大。直接用 HTTP 源站/CDN 当种子更简单。

### 片源怎么产生

- **首映 / 同步观看**：片源就是站内已有的种子文件，不需要转码，浏览器继续用 WebTorrent 播。
- **24/7 频道 / 节目单**：
  - 服务端 ffmpeg 按节目单读取已完成下载的本地文件，`-re` 实时输出 HLS（2–4 s 分片，GOP 对齐，`program_date_time`，`delete_segments`）。
  - 动画资源多数是 HEVC 10-bit 或 MKV 加 ASS 字幕，浏览器兼容性差，基本都要转成 H.264 8-bit + AAC，字幕要么烧进画面，要么转 WebVTT（会丢掉 ASS 特效）。
  - 1080p x264 veryfast 实时转码大约要 2–4 个核心；源本身是 H.264/AAC 时可以 `-c copy`，几乎不占 CPU。
- **真正的电视或直播流采集**（"放送同步"）：技术上就是把采集卡 / 流输入 ffmpeg，后面流程不变。
- **法律提示**：自建频道转播电视或版权动画，属于向公众传播或表演。站点成了直接发送内容的一方，而不再只是索引种子，法律风险明显高于现状，P2P 并不能降低这种风险。本文不提供采集或规避方面的做法，建议只用于有授权或无版权的内容，或者仅限私有的小圈子。

## 4. 原型（/tmp/live-lab，可复现）

- 组成：
  - `nix-shell -p ffmpeg`（8.1.1）：`testsrc2` 854×480@30 + 正弦音频 → libx264 1.5 Mbit/s，`-g 60`，HLS 2 s 分片，播放列表 15 个，开 `program_date_time`。
  - `server.py`：静态服务，统计源站发出的分片字节数。
  - `www/index.html`：hls.js 1.7.3 + p2p-media-loader-hlsjs 4.0.0，`announceTrackers: ["wss://tracker.projectk.org"]`。
  - `www/duo.html`：同一页里放 2 个 iframe 播放器，第二个晚 20 s 加入。
  - `driver2.mjs`：通过 CDP 每 10 s 采样一次。
  - 浏览器只开了一个独立的 headless chromium，结束后已关闭；ffmpeg 和 server 也已停止。
- 指标：
  - P2P 占比 = Σp2p / Σ(http+p2p)，来自 `onChunkDownloaded`；
  - 墙钟延迟 = `Date.now() - hls.playingDate`。
- 结果（2 个观众，同一台机器。WebRTC 走 host candidate，属于理想网络，真实环境会更差）：

| 设置 | 延迟（墙钟） | 采样时长 | P2P 占比 | 卡顿 |
|---|---|---|---|---|
| `liveSyncDurationCount=2`（目标 4 s），`highDemandTimeWindow=2` | 5.2–5.3 s | 150 s | **6.3%** | 0 |
| `liveSyncDurationCount=5`（目标 10 s），`highDemandTimeWindow=4` | 10.6–11.5 s | 180 s | **45.8%**（上限 50%） | 0 |
| p2pml 自动设置（目标 28 s；第一个人入场时播放列表还短，停在 6 s） | 6.5 s / 28.2 s | 180 s | **37.3%**（落后者 57% 来自 P2P） | 0 |

- 结论：
  - 我们的 tracker 能直接给 p2pml 做信令，一次就连上了。
  - 延迟约 5 s 时 P2P 基本无效；约 10 s 时，2 人场景已经接近理论上限。
  - 观众之间位置不同步并不妨碍分享，落后的人会从领先的人那里拿分片。
- 原型中踩到的坑：
  - headless 下后台 tab 的视频会被暂停，所以改成同一页放多个 iframe。
  - CDP 的 `/json/new?` URL 必须整体 `encodeURIComponent`，否则 query 参数会丢失。

## 5. 给本站的建议

### 第一步：同步观看房间 / 首映（3–5 人日）

- 复用：现有 WebTorrent 播放器（OPFS、SW）、wss tracker、qB WebRTC 种子 + WebSeed、ArtPlayer、cookie 鉴权和路由权限、现有的 SSE 基础设施。
- 新增内容：
  - 后端：房间（host、成员、片源 hash/file、状态 `{playing, position, at}`），通过 SSE/WebSocket 广播，权限沿用"能播这个文件才能进房间"。
  - 前端：host 的 play/pause/seek 同步给所有人；按服务器时钟纠偏，偏差小于 0.5 s 时调 `playbackRate` 0.95–1.05 慢慢追，偏差大于 2 s 时直接 seek。可选加弹幕或聊天。
  - 首映 = 房间加预约开播时间。
- 收益：所有观众看同一个位置，WebTorrent swarm 和 piece 局部性最好，完全不需要转码。延迟就是"同步误差"，可以做到 1 s 以内。

### 第二步（按需）：24/7 频道 / 节目单（约 1.5–3 人周）

- 后端（Go）：
  - 节目单加 ffmpeg 播出进程（每个频道 1 个），写 HLS 到 tmpfs 或磁盘；
  - 带鉴权的 m3u8 和分片路由（cookie + 权限），外加分片 sha256 接口；
  - 鉴权后下发 HMAC 形式的 `swarmId`；
  - 盯住磁盘：当前只剩 4.6 G，HLS 窗口要小，并且用 tmpfs。
- 前端：新增 `HlsP2PEngine`，和 `webtorrentEngine` 平级，用 ArtPlayer `customType.m3u8` 接 hls.js + `HlsJsP2PEngine.injectMixin`。设置：
  - `announceTrackers: [wss://tracker.projectk.org]`；
  - `liveSyncDurationCount` 取 5–8（4 s 分片，延迟 20–30 s）；
  - `highDemandTimeWindow` 取 4–8；
  - `validateP2PSegment` 做 sha256 校验；
  - iOS 17.1 以下回退原生 HLS（无 P2P）。
- 成本估算：1080p 约 5 Mbit/s，100 人同时看，不开 P2P 时源站约 500 Mbit/s。参照 PeerTube 的 75% 分流，大约 125 Mbit/s。人数很少时分流有限，例如 3 人最多只能省 67%。

### 风险

- 法律风险：转播或频道化放大了站点的直接传播责任，P2P 只能缓解带宽，不能缓解法律风险。
- 转码 CPU：HEVC、ASS 字幕都要处理。
- 移动端和 NAT 环境下分流明显低于压测数据，iOS 低版本完全没有 P2P。
- p2pml 大版本经常有不兼容变化：v2.3 改过 infohash，v5 已经在 alpha，需要锁定版本。
- 开放 tracker 下 swarm 可能被他人发现或投毒，需要 HMAC swarmId 加 sha256 校验来缓解。
- 和 WebTorrent VOD 共用一个 tracker，会增加 tracker 负载（每个观众一条 WebSocket，每个 swarm 有 offers）。

### 需要站长决定的问题

1. "直播"到底指什么：同步观看 / 首映（推荐，最便宜）、24/7 节目单频道，还是真正的电视同步转播（法律风险最高，不建议）？
2. 能接受多少延迟？10 s 以下基本没有 P2P 收益，20–30 s 是比较好的平衡点。
3. 同时在线的规模预计多大？源站上行带宽和 CDN 预算是多少？小规模时 P2P 收益有限，可能直接 HTTP 就够了。
4. 片源是否允许转码（CPU 预算）？字幕选烧录还是 WebVTT？
5. 房间和频道的权限模型：沿用"能播该文件"，还是单独开一个直播权限？要不要邀请制？
6. 现有 tracker 有没有开 infohash 白名单或限流？能否给 p2pml swarm 单独开放？

## 来源

- BEP 39/44/46/0：https://www.bittorrent.org/beps/bep_0039.html 、bep_0044.html 、bep_0046.html 、bep_0000.html
- WebTorrent browser 字段（npm registry `webtorrent@3.0.21`）、FAQ：https://github.com/webtorrent/webtorrent/blob/master/docs/faq.md
- p2p-media-loader：https://github.com/Novage/p2p-media-loader（README、FAQ、releases、`packages/p2p-media-loader-core/src/core.ts`、`types.ts`）
- PeerTube：https://joinpeertube.org/news/stress-test-2023 、https://joinpeertube.org/news/release-4.2 ；源码 `client/src/standalone/player/src/shared/player-options-builder/hls-options-builder.ts`、`server/core/initializers/constants.ts`（`SEGMENT_TIME_SECONDS`）、`server/core/lib/live/live-segment-sha-store.ts`；issue #3494、#5412
- live-torrent：https://github.com/pldubouilh/live-torrent ；HN 16422248 / 14443968（Nile.js）
- BitTorrent Live：HN 3588453（TechCrunch 2012-02-13）、5362278（2013-03 open beta）、11929759（btlive.tv 2016）
- Peer5：HN "Launch HN: Peer5 (YC W17)"（2017-01-27）、Microsoft eCDN 公告（techcommunity，2022-09）、Calcalist / TechTarget 2021-08 收购报道
- Streamroot / Lumen：Wikipedia "Lumen Technologies"（2019-09-10 收购；2023-10 把 CDN 合同卖给 Akamai）
- SwarmCloud / CDNBye：https://github.com/cdnbye/hlsjs-p2p-engine
- RFC 7574（PPSPP）；Hei et al., "A Measurement Study of a Large-Scale P2P IPTV System", IEEE Trans. Multimedia 2007
- npm 周下载：api.npmjs.org/downloads/point/last-week/<pkg>（2026-10-19）
