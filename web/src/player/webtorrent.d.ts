// webtorrent@3 预打包 ESM 没有类型声明；这里只声明本项目用到的子集。
declare module "webtorrent/dist/webtorrent.min.js" {
  export interface WTWire {
    type: string
    remoteAddress?: string
    remotePort?: number
    peerId?: string
    downloadSpeed(): number
    uploadSpeed(): number
    destroy(): void
    /** 对方宣告有的分块（WebSeed：internals.patchWebSeed 宣告的服务器已有分块） */
    peerPieces?: { get(i: number): boolean }
    on(ev: "bitfield" | "have", cb: () => void): void
  }
  export interface WTPiece {
    length: number
    missing: number
  }
  export interface WTFile {
    name: string
    path: string
    length: number
    progress: number
    done: boolean
    _startPiece: number
    _endPiece: number
    offset: number
    readonly streamURL: string
  }
  export interface WTTorrent {
    infoHash: string
    name: string
    magnetURI: string
    length: number
    pieceLength: number
    pieces: (WTPiece | null)[]
    bitfield?: { get(i: number): boolean }
    files: WTFile[]
    wires: WTWire[]
    progress: number
    downloaded: number
    uploaded: number
    downloadSpeed: number
    uploadSpeed: number
    numPeers: number
    timeRemaining: number
    done: boolean
    ready: boolean
    destroyed: boolean
    torrentFileBlob: Blob | null
    select(start: number, end: number, priority?: number): void
    deselect(start: number, end: number): void
    critical(start: number, end: number): void
    addWebSeed(url: string): void
    destroy(opts?: { destroyStore?: boolean }, cb?: (err?: Error) => void): void
    on(ev: "metadata" | "ready" | "done" | "infoHash", cb: () => void): void
    on(ev: "error" | "warning", cb: (err: Error | string) => void): void
    /** 分块校验通过（_markVerified） */
    on(ev: "verified", cb: (index: number) => void): void
    on(ev: "wire", cb: (wire: WTWire, addr?: string) => void): void
    on(ev: "evicted", cb: (index: number) => void): void
    removeAllListeners(): void
    removeListener(ev: string, cb: (...args: never[]) => void): void
  }
  export interface WTClientOptions {
    tracker?: { rtcConfig?: RTCConfiguration }
  }
  export default class WebTorrent {
    constructor(opts?: WTClientOptions)
    torrents: WTTorrent[]
    add(
      id: string | Uint8Array,
      opts?: {
        announce?: string[]
        deselect?: boolean
        /** abstract-chunk-store 构造器：new store(pieceLength, { ...storeOpts, torrent, length, name, … }) */
        store?: new (chunkLength: number, opts: never) => unknown
        storeOpts?: Record<string, unknown>
        storeCacheSlots?: number
        /** 启动 bitfield（长度 ceil(numPieces/8)） */
        bitfield?: Uint8Array
      },
      ontorrent?: (t: WTTorrent) => void,
    ): WTTorrent
    get(id: string): Promise<WTTorrent | null>
    createServer(opts: { controller: ServiceWorkerRegistration }): unknown
    on(ev: "error" | "warning", cb: (err: Error | string) => void): void
  }
}

declare module "torrent-piece" {
  export default class Piece {
    constructor(length: number)
    length: number
    missing: number
  }
}
