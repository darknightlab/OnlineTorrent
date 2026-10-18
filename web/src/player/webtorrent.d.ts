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
    torrentFileBlob: Blob | null
    select(start: number, end: number, priority?: number): void
    critical(start: number, end: number): void
    addWebSeed(url: string): void
    destroy(opts?: { destroyStore?: boolean }, cb?: (err?: Error) => void): void
    on(ev: "metadata" | "ready" | "done" | "infoHash", cb: () => void): void
    on(ev: "error" | "warning", cb: (err: Error | string) => void): void
    on(ev: "wire", cb: (wire: WTWire, addr?: string) => void): void
    removeAllListeners(): void
  }
  export interface WTClientOptions {
    tracker?: { rtcConfig?: RTCConfiguration }
  }
  export default class WebTorrent {
    constructor(opts?: WTClientOptions)
    torrents: WTTorrent[]
    add(id: string | Uint8Array, opts?: { announce?: string[] }, ontorrent?: (t: WTTorrent) => void): WTTorrent
    get(id: string): Promise<WTTorrent | null>
    createServer(opts: { controller: ServiceWorkerRegistration }): unknown
    on(ev: "error" | "warning", cb: (err: Error | string) => void): void
  }
}
