# 浏览器端按 piece 存储 + 预算淘汰：可行性调研

调研日期：2026-10-18。环境：Chromium 154 headless（`--headless=new`），WebTorrent 3.0.21，bittorrent-protocol 5.0.9，lt_donthave 2.0.7。
原型和原始数据都在 `/tmp/chunkstore-lab`，没有改任何生产代码：`bench*.mjs`、`quota-run.mjs` 和 `proto/{piece-store.js,run.mjs}`。

## TL;DR

- **后端选 OPFS，每个 piece 一个文件**，目录结构为 `ot-pieces/<infohash>/<index>`。主线程直接用异步 API（`createWritable` 写，`getFile().slice()` 读）。实测 quota 只按实际写入的字节计（6000 MB 数据对应 usage 6000.2 MB）。删除文件后 usage 和磁盘占用都**立即**下降，删一个 piece 约 0.5–2 ms（16 MiB 的 piece 约 9 ms）。写入 60–136 MB/s，读取 100–400 MB/s，比浏览器 P2P 的速度高两个数量级。Worker 加 SyncAccessHandle 只快 1.2–2 倍，复杂度却明显更高，先不做。
- IndexedDB（一条记录一个 piece）也能用，quota 计账同样准确，但删除慢（约 20–30 ms/条），16 KiB 的随机块读要先读出整个 piece（16 MiB 的 piece 只有 10 ops/s）。IDB 存 Blob、Cache API 两种方式都有 **usage 不能及时释放** 的问题（`deleteDatabase` 或 `caches.delete` 之后 usage 还停在 256–512 MB），排除。
- npm 上找不到带预算或淘汰的现成 chunk store。`@thaunknown/idb-chunk-store`、`hybrid-chunk-store` 这些都只是普通的 KV 存储，不值得引入，**自己写一个大约 300 行的 store**。
- 在 WebTorrent 3 里淘汰一个已校验的 piece 是**可行的**，原型已验证：被淘汰的区间重读时会自动重新下载，字节一致。但要动这些内部状态：`bitfield`、`pieces[i]`（换成新的 Piece）、`_reservations`、`file.done`、`torrent.done`。另外有两个**必须绕开的坑**：
  1. 原生 `FileIterator` 会把从起点到 EOF 整段都 select 上，而且 selection 的 offset 只增不减。预算小于文件大小时，它会**无限超前下载再被淘汰**（下载量最高达到读取量的 13 倍），并且在 piece 被淘汰后**永久卡住**。解决办法是自己实现窗口化的 iterator 替换 `file[Symbol.asyncIterator]`。这样还能顺带拿到精确的读位置（第 4 节）。
  2. 远端请求我们已经淘汰的 piece 时，WebTorrent 直接**不回应**，对方等 30 s 后断开连接。如果改成 reject，远端 WebTorrent 会对同一个 piece **死循环重试**（35 s 内 5000 多次）。如果我们曾经发过 HAVE_ALL，对方收到 lt_donthave 也会忽略。正确做法是三件一起做：**不发 HAVE_ALL（改发 bitfield）+ 淘汰时发 lt_donthave + 对缺失的 piece 回 reject**。实测远端只拉没被淘汰的 70/80 个 piece，0 次 reject，连接保持。
- 写入时遇到 QuotaExceededError 不能交给 WebTorrent 处理（它会直接 destroy 种子）。原型的做法：预算自动收缩到当前占用的 90%，淘汰后重试一次；还失败就先把数据暂存在内存，回调成功，几秒后再把这个 piece 标成未校验。实测 quota 比预算小时种子能存活，读出的数据一致。原生 fsa-chunk-store 在同样条件下进度到 43% 就被 destroy。
- 工作量估计 **5–7 人日**，要锁定 webtorrent 3.0.21 版本，见第 6 节。

---

## 1. 存储后端：性能与 quota 实测

每组写 512 MB 随机数据。W 是顺序写（并发 1）；R1/R4 是整 piece 读（并发 1/4）；blk 是 16 KiB 随机块读（并发 8，对应做种时的读法）；del 是每删一个 piece 的耗时。

| 后端 | piece | W MB/s | R1 / R4 MB/s | blk ops/s | del ms/个 | 删除后 usage |
|---|---|---|---|---|---|---|
| OPFS async（主线程） | 256K | 23 | 109 / 143 | 1008 | 0.44 | 立即释放 |
| | 1M | 76 | 191 / 217 | 2252 | 0.73 | 立即释放 |
| | 4M | 60 | 181 / 245 | 1564 | 2.2 | 立即释放 |
| | 16M | 73 | 365 / 401 | 2152 | 8.9 | 立即释放 |
| OPFS sync（Worker，不 flush） | 256K | 51 | 79 / 175 | 1328 | 0.69 | 立即释放 |
| | 1M | 122 | 213 / 237 | 1888 | 1.1 | 立即释放 |
| | 4M | 96 | 182 / 196 | 786 | 2.5 | 立即释放 |
| | 16M | 94 | 210 / 197 | 1255 | 7.0 | 立即释放 |
| IDB，值为 Uint8Array | 256K | 13 | 108 / 162 | 773 | 21.7 | 立即释放 |
| | 1M | 43 | 125 / 171 | 189 | 25.5 | 立即释放 |
| | 4M | 42 | 187 / 215 | 49 | 29.3 | 立即释放 |
| | 16M | 66 | 159 / 154 | **10** | 26.6 | 立即释放 |
| IDB，值为 Blob | 1M–16M | 59–86 | 304–658 | ~2000 | 11–20 | **滞后，`deleteDatabase` 后仍有 508 MB 没释放** |
| Cache API | 1M–16M | 30–93 | 235–443 | ~1800 | 0.3 | 逐条删除会释放，**`caches.delete()` 后不释放** |

补充数据：
- sync handle 每次写都 `flush()` 的话，1 MiB 的 piece 写入只有 26 MB/s（不 flush 是 122 MB/s）。同一个文件不能同时开两个读写 handle，读取时要用 `{mode:"read-only"}`。
- 每个文件的计账开销约 150 B（2048 个 256 KiB 文件，usage 512.3 MB）。`createWritable` 写到 swap 文件里，`close()` 时才提交，是原子的：写到一半失败不会留下数据。
- **规模测试：1500 个 4 MiB 的 piece，共 6000 MB**，写并发 2：

| | 写 | 读（并发 4） | 列出 1500 个文件名 | 列文件名并取大小 | 删 500 个 |
|---|---|---|---|---|---|
| OPFS async | 136 MB/s（44 s） | 103 MB/s | 45 ms | 540 ms | 0.96 ms/个 |
| OPFS sync | 169 MB/s（35 s） | 156 MB/s | 64 ms | 621 ms | 0.78 ms/个 |
| IDB | 56 MB/s（107 s） | 162 MB/s | getAllKeys 4 ms | – | 23 ms/个 |

  三种后端写完后 usage 都是 6000.2 MB，`du` 显示 6011 MB。删除 500 个后 usage 降到 4000.1 MB，`du` 显示 4042 MB。
- **quota 强制测试**：用 CDP `Storage.overrideQuotaForOrigin` 把 quota 设为 200 MB，写 4 MiB 的 piece。四种后端都在写到第 49 个（196 MB）时抛 `QuotaExceededError`。删掉 10 个后 usage 立即降到 156 MB，紧接着再写 10 个全部成功。也就是说删除**同步释放 quota**，之后马上可以复用。
- **quota 数值观察**：headless 下 `estimate().quota` 一直等于 `usage + 10240 MB`（usage 为 0 时是 10240，为 6000 时是 16240）。也就是说 quota 会随 usage 变化，而且 CDP override 不会反映到 `estimate()` 上。结论是 `estimate()` 只能当估计值用，**真正的硬上限要以 QuotaExceededError 为准**。

### 结论与浏览器支持（根据文档，没有实测）
- **Chrome/Edge**：OPFS 从 86 起可用，`createWritable` 从 86 起，SyncAccessHandle 从 102 起。单个 origin 最多能用磁盘的约 60%。best-effort 存储在磁盘吃紧时可能被整体清掉，可以调 `navigator.storage.persist()` 降低风险，但 store 也必须能容忍目录突然消失。
- **Firefox**：111 起支持 OPFS，包括 `createWritable` 和 Worker 里的 SyncAccessHandle。best-effort 的上限是磁盘的 10% 与 10 GiB（按 eTLD+1 分组）中较小的一个，比 Chrome 小很多。预算公式 `25% × quota` 正好能覆盖这种情况。
- **Safari**：15.2 起有 OPFS（getDirectory/getFile），SyncAccessHandle 只能在 Worker 里用。`createWritable` 要到很新的版本才有。WebTorrent 自己判断 `SUPPORTS_FSA` 时要求有 `createWritable`，所以老版本 Safari 会退回 MemoryChunkStore。17 起配额约为磁盘的 60%。ITP 会在用户 7 天没有交互后清掉脚本写入的存储。
- **推荐方案**：主路径用 OPFS async。没有 `createWritable` 时，Safari 走 Worker + SyncAccessHandle（同一套目录结构，后面再做），再不行就退回 IDB 一条记录一个 piece，最后才是内存。
- 现有的包：`@thaunknown/idb-chunk-store` 1.0.4（一个 DB 对应一个种子，没有淘汰），`hybrid-chunk-store` 1.2.6（fsa/idb 加内存拼接），`cache-storage-chunk-store`、`idbkv-chunk-store`、`indexeddb-chunk-store`（2017–2021，已停止维护）。**都没有预算或淘汰功能**，最多当接口参考。另外发现 fsa-chunk-store 的目录名是 `<name> - <ih8>`，同一个种子在两个 client（或两个 tab）里会共用同一个稀疏大文件。原型中一个 leecher 直接把 seeder 写进 OPFS 的数据当成了自己已有的，"秒完成"。

## 2. WebTorrent 3 对 abstract-chunk-store 的要求

在 `lib/torrent.js` 里：
- 通过 `client.add(magnet, { store: Ctor, storeOpts, storeCacheSlots, deselect, bitfield })` 传入。store 用 `new Ctor(pieceLength, { ...storeOpts, torrent, path, files, length, name, addUID, rootDir, max })` 构造，构造发生在元数据解析完、`emit('metadata')` 之前，所以构造时已经能拿到 `opts.torrent.infoHash`。另外还可以传 `preloadedStore` 实例。
- 外面会套两层包装：先是 `CacheChunkStore`（LRU 缓存 `storeCacheSlots` 个完整 piece，默认 20；如果 store 是 MemoryChunkStore 则不套），再是 `ImmediateChunkStore`（put 完成前由内存应答 get）。必须提供 `chunkLength` 属性。CacheChunkStore 调 `get(i, cb)` 时**不带 opts**，会读整个 piece 再自己切片。piece 是 16 MiB 时，20 个槽就是 320 MB 内存，建议把 `storeCacheSlots` 设成 2–4。
- `put(i, buf, cb)`：只在 SHA1 校验通过后调用，buf 是完整的 piece（最后一个可能较短）。**`cb(err)` 会触发 `torrent._destroy(err)`，是致命的**，所以 store 永远不能把 quota 错误往上抛。
- `get(i, {offset,length}|null, cb)`：启动校验（`_verifyPiece`）时出错只会把该 piece 当作未校验，是安全的。`FileIterator` 里出错会 destroy 迭代器，流提前结束，`<video>` 收到截断的响应。peer 请求时出错，有 fast 扩展就回 reject，否则不回应。
- `close(cb)` / `destroy(cb)`：调用 `torrent.destroy({destroyStore})` 时触发。现在 engine 一律传 `destroyStore:true`。要做 LRU 缓存就得改成 `close`，数据交给预算管理器回收。
- **store 能不能报告"没有这个 piece"？** 接口里没有 `has()`。只有在**启动校验阶段** get 报错是安全的（piece 会被标为缺失）。种子 ready 之后，store 不能悄悄丢数据，必须先调 `unverifyPiece()`（第 3 节）修改 bitfield，再删文件。
- 续传：原型的 R 实验里，重启后 store 中有 80 个 piece，ready 时把它们全部重新算了一遍哈希（20 MiB 用了 308 ms）。6 GB 的话要几分钟。正式实现应当根据目录里的文件名生成 `bitfield` 传给 `add()`。这样就走 `_verifyPiecesUsingBitfield`，每个文件只抽查 1–2 个 piece。这种做法成立的前提是只写校验过的数据，并且写入是原子的。

## 3. 淘汰一个已校验的 piece：需要改的内部状态（已验证）

```js
function unverifyPiece(t, i) {            // 原型 proto/piece-store.js
  if (!t.bitfield.get(i)) return
  t.bitfield.set(i, false)
  t.pieces[i] = new PieceCtor(len(i))     // 必须是新的 Piece：_request/_updateWireInterest/peer 请求都按 pieces[i] 是否为 null 判断
  t._reservations[i] = []                 // 校验后会被置 null；_request 能兼容 null，这里只是整理一下
  for (const f of t.files) if (f.done && f.includes(i)) f.done = false   // 否则 file 迭代器走 chunkStoreRead，读到缺失 piece 直接报错
  t.done = false                          // 之后 _checkDone 会再次 emit 'done' 和 discovery.complete()（无害，但 UI 会重复记日志）
  for (const w of t.wires) w.lt_donthave?.donthave(i)
}
```
- `PieceCtor`：dist bundle 没有导出这个类。原型在 `'metadata'` 事件里从 `torrent.pieces.find(Boolean).constructor` 取到。正式实现可以直接 `import Piece from 'torrent-piece'`（4.0.1，WebTorrent 内部没有用 instanceof 判断）。`torrent._markUnverified(i)` 也会建新的 Piece，但它会 `select(i,i)`，导致后台立刻重下，不合适。
- 不用动的部分：`_rarityMap` 只统计远端 peer 的情况，与我们的 bitfield 无关。`torrent.downloaded`、`progress`、`file.progress` 都是从 bitfield 现算的，会自动变小（实验 A 中进度从 1 变成 0.875）。`_critical[i]` 一直不会被清掉，没有影响。CacheChunkStore 里可能还缓存着这个 piece，数据已经校验过，可以继续给出，也可以手动 `torrent.store.store.cache.remove(i)` 清掉。
- **selection**：`_gcSelections` 只会让 `s.offset` 增加，请求总是从 `from+offset` 开始找。所以 offset 之前被淘汰的 piece **不会再被请求**，除非有新的 selection 覆盖它。

原型（一个页面里跑 seeder S、downloader D 两个 client，本地 ws tracker，WebRTC 走回环；文件 20 MiB，分 80 个 256 KiB 的 piece）：

| 实验 | 结果 |
|---|---|
| A：全部下完后淘汰 20..29，再用 `createReadStream` 重读 | 3 s 内没有自发重下（receivedDelta=0）。读取时重新下载了 2,623,520 B，正好是 10 个 piece，**字节一致**。`done` 又变回 true，整个文件的 sha 一致 |
| B：读到第 0 个 piece 的原生 FileIterator 停住，等种子 done 后淘汰 40..44 再接着读 | 读到 piece 40 **卡住 10 s 以上，期间一个字节都没请求**（selection 已经被 gc 掉）。换成窗口化 iterator：读完全文件，sha 一致，只重下了被淘汰的部分 |
| C：预算 6 MiB，以 1 MB/s 速度顺序读 20 MiB | **原生 iterator**：读到 piece 17/18 时卡死，两次运行分别下载了 20 MB 和 59.8 MB，但只读出 4.5 MB，被淘汰的几乎都是 ahead 部分。**窗口化 iterator**：完整读完，sha 一致，总下载量 20.0 MB（没有重复下载），最大存储 6.00 MB，56 次淘汰全部属于 behind，OPFS 里剩 24 个文件，usage 6 MB |
| D：远端 L 已经知道 D 有某 piece，D 淘汰后 L 来请求（S 已退出） | ① 什么都不做：WebTorrent 收到请求时检查 `if (this.pieces[index]) return`，不回应，L 等 30,010 ms 后断开 wire（PIECE_TIMEOUT）。② 只回 reject：L 对 piece 0 死循环，35 s 内 5176 次 reject，进度为 0。③ 发 donthave 加 reject，但之前发过 HAVE_ALL：同样死循环（4840 次），因为 `HaveAllBitField.set()` 是空操作。④ **donthave + reject + 用 bitfield 代替 HAVE_ALL**：L 拿到全部 70 个没被淘汰的 piece，0 次 reject，连接保持 |
| E：quota 设为 usage+8 MB，预算 64 MB | 原生 fsa-chunk-store：进度 43% 时 `QuotaExceededError`，**种子被 destroy**。原型 store：只遇到 1 次 quota 错误，预算自动收缩到 7.2 MB，种子存活，全文件读出一致 |

处理 wire 的代码（在 `torrent.on('wire')` 里同步注册，早于 `_onWireWithMetadata`）：
```js
wire.haveAll = () => wire.bitfield(torrent.bitfield)        // 远端才能接受之后的 lt_donthave
wire.on('request', (i, o, l, cb) => { if (torrent.pieces[i]) cb(new Error('evicted')) })  // fast 扩展会转成 reject
```
远端如果既不支持 lt_donthave 也不支持 fast 扩展，就只能等它 30 s 超时断开。这种情况很少见：libtorrent/qBittorrent 两者都支持。

用到的 WebTorrent 内部接口，升级前必须重新核对：`bitfield`、`pieces`、`_reservations`、`_selections`、`_select`/`_deselect(…, isStreamSelection)`、`critical`、`files[].done`/`_startPiece`/`_endPiece`/`offset`、`'verified'` 事件、`store.get`、`wire.haveAll`/`lt_donthave`/`'request'`。

## 4. 读位置的获取方式

调用链：SW 拦截 fetch，转给 BrowserServer.wrapRequest，再到 `ServerBase.serveFile`，它调用 **`file[Symbol.asyncIterator](range)`**（`lib/server.js:153`），然后 `emit('iterator', …)`，再交给 `Readable.from`。SW 每 `pull` 一次，页面调用一次 `iterator.next()`，所以迭代器的位置与 `<video>` 实际消费到的位置相差不超过 1–2 个 chunk（streamx 有缓冲）。

**建议的挂钩点**：在 `'metadata'` 时，对每个 File 实例覆盖 `file[Symbol.asyncIterator] = (opts) => windowedIterator(budget, file, opts)`。这样 serveFile、createReadStream、arrayBuffer 都会走我们的实现，文件已经 done 时走的 chunkStoreRead 分支也覆盖到了。
- 不用 `'iterator'` 事件的原因：事件触发时原生 FileIterator 已经创建好，并且已经 select 了 start..EOF，只能先销毁再替换。
- 也不建议包装 FileIterator 的 `_piece`：文件 done 时根本不会创建 FileIterator。
- 窗口化 iterator（原型约 60 行）的做法：
  - `reader = {file, start, pos, end}` 登记到 `budget.entry(ih).readers`，`return()` 时注销，torrent destroy 时也要注销。
  - 每次 `next()` 维护一个滑动的 stream selection `[cur, cur+window]`，替代原来的 `[start, EOF]`。
  - piece 不在时就 `critical()` 并等 `'verified'`。get 失败就 `unverifyPiece()` 后重新等待，不会让流断掉。
- 可以把 `video.currentTime`、`video.buffered` 作为辅助信号（例如暂停很久后适当缩小 behind 保护区）。但容器的字节偏移只有 iterator 知道。

## 5. 设计方案

**模块**（`web/src/player/store/`）：
- `opfs-piece-store.ts`：abstract-chunk-store 实现。目录为 `ot-pieces/<ih>/<index>`，另有一个 `ot-pieces/<ih>/meta.json` 记录 `{name, length, pieceLength, lastUsed}`。put 先向预算管理器申请空间（`reserve()` 时**立即计入**，避免并发写超出预算；原型里修正这一点之前曾超出 0.75 MB），再写文件；get 读 `getFile().slice()`。
- `budget.ts`：每个 tab 一个单例，保存 `Map<ih, {bytes, pieces:Set, lastUsed, live torrent?, readers}>` 和 `legacyBytes`。
- `evict.ts`：`unverifyPiece` 和 wire 处理（第 3 节）。
- `windowed-iterator.ts`：第 4 节的实现。
- `webtorrent-engine.ts` 要做的改动：`add()` 时传 `store`、`storeCacheSlots: 4`、`bitfield`（根据扫描结果生成）。如果 `torrent.length > 0.8 × budget`，用 `deselect: true` 加首尾 piece 优先，只靠窗口下载；否则保留现在的整种后台下载（对 swarm 更友好）。session 结束时用 `close`，不再用 `destroyStore`。

**预算**：`budget = min(site.max_push_size_bytes, 0.25 × estimate().quota)`，后端已经在 `/site` 里提供了 `max_push_size_bytes`。
- 何时刷新：启动时；之后每 5 分钟或每写入 256 MB 一次。
- `estimate()` 是异步的近似值，而且 quota 会随 usage 变化（实测 quota = usage + 10 GiB）。因为 25% 本身就留了余量，只需要再保证预算不超过 `quota − 其他 usage − 安全余量`。
- 遇到 QuotaExceededError 时，把预算降为 `max(下限, 当前占用 × 0.9)`，淘汰后重试一次。仍然失败就把数据放进内存 Map，回调成功，5 s 后对该 piece 调 `unverify`，绝不让 put 报错。
- 预算下限要能容纳受保护的部分：读前窗口 + 首尾 piece + 正在读的 piece。预算不够时先缩小窗口。

**淘汰顺序**（在 `reserve()` 里循环执行，直到 `total + size ≤ budget`）：
1. legacy 条目：OPFS 根目录下除 `ot-pieces` 以外的所有内容（fsa-chunk-store 的 `<name> - <ih8>` 目录、`chunks`）。大小按 `getFile().size` 累加，这个逻辑大小正好就是 Chrome 计费的数值。启动时，最近修改时间早于 10 分钟的直接删掉，其余的按需删除。
2. 当前 tab 没在播放、也没被其他 tab 占用的种子，按种子整体 LRU（`meta.json` 里的 `lastUsed`）整个目录删除。
3. 正在播放的种子里，读位置之后的 piece 中距离最远的。
4. 读前窗口之外的 piece 中距离最远的。

始终保护：每个 reader 的 `[pos − behind, pos + ahead]`，建议 ahead = max(32 MiB, 60 s 码率)，behind 约 8 MiB；正在播放文件首尾各 2 个 piece 且不少于 8 MiB（MP4 的 moov、MKV 的 cues）；正在 get 的 piece。全部受保护时允许暂时超出预算，此时由 quota 错误路径兜底。按稀有度淘汰留作 TODO：可以把 `_rarityMap` 的值作为同等距离下的次级排序键。

**元数据与启动**：文件系统本身就是索引。启动时列目录名，1500 个 piece 约 45 ms；不需要逐个取大小，大小由 `meta.json` 里的 pieceLength 推出。
- 每个 piece 的最近使用时间不需要持久化：播放中的种子按位置排序，其他种子按整体 LRU。
- `lastUsed` 在打开、关闭种子时写入，播放过程中每 30 s 节流写一次。
- `meta.json` 缺失或损坏的目录当作 legacy 处理。

**刷新页面与多 tab**：
- 刷新：页面卸载时不保证能执行任何清理代码，但数据都在 OPFS 里，下次启动扫描后可以用 `bitfield` 续传，不需要额外处理。
- 多 tab：每个 tab 打开种子时持有 `navigator.locks.request('ot:'+ih)`。预算管理器通过 `navigator.locks.query()` 跳过被其他 tab 占用的种子。同一个种子在第二个 tab 打开时，用独立的临时命名空间 `ot-pieces/<ih>~<tabId>`，关闭或下次启动时清理，避免两个 tab 各自的 WebTorrent 状态互相冲突。预算是各 tab 各算各的，但都以 OPFS 的实际内容为准：写入前都会扫描或计入，多 tab 同时播放时可能短暂超出预算，靠 quota 错误兜底。
- 如果另一个 tab 删掉了我们正在用的 piece，get 失败后走 `unverifyPiece` 并重新下载，原型已覆盖这种情况。

**风险**：
- 依赖 WebTorrent 内部实现，需要锁定版本并加回归测试。
- 不发 HAVE_ALL 后，对 seeder 的识别与之前略有不同。
- `'done'` 事件会重复触发：engine 的日志要去重，UI 进度会下降，需要把"已缓存"和"已下载"分开显示。
- 窗口模式下 swarm 拿到的完整副本更少：种子大于预算时，本地本来就存不下整个种子。
- Firefox 的 quota 更小。Safari 缺少 `createWritable`，需要走 Worker 路径。
- best-effort 存储可能被浏览器整体清掉。

**工作量**：
| 内容 | 人日 |
|---|---|
| store + 预算 + 扫描与 legacy 清理 | 1.5 |
| 淘汰钩子、窗口 iterator、wire 处理、selection 策略、bitfield 续传 | 2 |
| 多 tab 锁、engine 与 UI（缓存进度、日志）调整 | 1 |
| 把原型转成自动化测试（headless 两个 client + 本地 tracker） | 1.5 |
| Safari Worker 路径（可选） | +1 |

合计 **5–7 人日**。
