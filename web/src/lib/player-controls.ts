// Player control policy (sm = 640px). A phone remains a phone when rotated in fullscreen.
export function phoneControls(width: number, phoneDevice: boolean): boolean {
  return phoneDevice || width < 640
}

export function centerSender(fullscreen: boolean, phone: boolean, width: number, height: number, freeWidth: number): boolean {
  return fullscreen && (!phone || width > height) && freeWidth >= 160
}

export interface DialogMedia {
  paused: boolean
  ended: boolean
  isConnected: boolean
  pause(): void
  play(): Promise<void>
}

/** A dialog owns a pause/resume ticket for one exact media element/source. Dismiss is the
 * only operation that resumes; disposal/file navigation only cancels. No async autoplay
 * from a previous file can be scheduled by cleanup. */
export class DialogPlayback {
  private ticket: { video: DialogMedia; source: string; playing: boolean } | null = null

  open(video: DialogMedia, source: string) {
    if (this.ticket) return
    this.ticket = { video, source, playing: !video.paused && !video.ended }
    video.pause()
  }

  close(video: DialogMedia, source: string) {
    const t = this.ticket
    this.ticket = null
    if (t?.playing && t.video === video && t.source === source && video.isConnected) void video.play().catch(() => {})
  }

  cancel() { this.ticket = null }
}
