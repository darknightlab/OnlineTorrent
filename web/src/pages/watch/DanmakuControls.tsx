// Shared by idle PlayerShell and every real ArtVideo, including /watch/hash. Settings and
// the button never depend on metadata/comments. Only a verified playing-file mapping may
// fetch/send episode comments (Playback's existing resolve navigation supplies it).
import { useCallback, useEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"
import { useLocation } from "react-router-dom"
import { Dialog, DialogContent } from "@/components/ui/dialog"
import { api } from "@/lib/api"
import { PERM, can, useMe } from "@/lib/queries"
import { loadDanmakuSettings, saveDanmakuSettings, type DanmakuSettings } from "@/lib/danmaku"
import { DialogPlayback } from "@/lib/player-controls"
import type { DanmakuItem } from "@/lib/types"
import type { ArtHandle } from "./artplayer-setup"
import type { DanmakuSource } from "./ArtVideo"
import { DanmakuSendBox, DanmakuSettingsForm } from "./DanmakuPanel"

let modulePromise: Promise<typeof import("./danmaku")> | null = null
const load = () => (modulePromise ??= import("./danmaku").catch((e) => { modulePromise = null; throw e }))

export function DanmakuControls({ handle: h, source, mediaKey, requested, onRequested }: {
  handle: ArtHandle
  source?: DanmakuSource | null
  mediaKey: string
  requested: boolean
  onRequested: (open: boolean) => void
}) {
  const me = useMe()
  const loc = useLocation()
  const [settings, setSettings] = useState(loadDanmakuSettings)
  const settingsRef = useRef(settings)
  settingsRef.current = settings
  const [center, setCenter] = useState(h.hasCenterSender())
  const [host, setHost] = useState<HTMLElement | null>(h.fullscreenHost())
  const [ready, setReady] = useState(false)
  const emitter = useRef<((item: DanmakuItem) => void) | null>(null)
  const playback = useRef(new DialogPlayback())
  const sourceKey = `${mediaKey}|${source?.provider ?? ""}/${source?.id ?? ""}/${source?.episode ?? ""}|${loc.pathname}`
  const latestKey = useRef(sourceKey)
  latestKey.current = sourceKey
  const read = can(me.data, PERM.danmaku)
  const send = can(me.data, PERM.danmakuSend)

  // A new file/route cancels the old dialog ticket without playing either the old or new
  // file. Unmount also only cancels (never resume from effect cleanup).
  useEffect(() => {
    const ticket = playback.current
    return () => { ticket.cancel(); onRequested(false) }
  }, [h, sourceKey, onRequested])

  useEffect(() => {
    if (requested) playback.current.open(h.video, sourceKey)
  }, [requested, h, sourceKey])
  const dismiss = useCallback((open: boolean) => {
    if (!open) playback.current.close(h.video, latestKey.current)
    onRequested(open)
  }, [h, onRequested])

  useEffect(() => {
    const update = () => { setCenter(h.hasCenterSender()); setHost(h.fullscreenHost()) }
    update()
    return h.onLayout(update)
  }, [h])

  useEffect(() => {
    if (!source || !mediaKey || (!read && !send)) return
    const ctl = new AbortController()
    let cancelled = false
    let shown: typeof import("./danmaku") | null = null
    const items = read ? api.danmaku(source.provider, source.id, source.episode, ctl.signal).then((r) => r.items, () => [] as DanmakuItem[]) : Promise.resolve([] as DanmakuItem[])
    void load().then(async (m) => {
      if (cancelled || h.art.isDestroy) return
      // Initialise immediately, not after the potentially slow platform collection, so
      // user comments can be sent even while collected comments are loading.
      m.showDanmaku(h.art, [], settingsRef.current)
      shown = m
      emitter.current = (item) => m.emitDanmaku(h.art, item)
      setReady(true)
      const list = await items
      if (!cancelled && !h.art.isDestroy) m.appendDanmaku(h.art, list)
    }).catch(() => {})
    return () => {
      cancelled = true
      ctl.abort()
      emitter.current = null
      shown?.clearDanmaku(h.art)
      setReady(false)
    }
  }, [h, mediaKey, source?.provider, source?.id, source?.episode, read, send])

  useEffect(() => {
    if (ready) void load().then((m) => { if (!h.art.isDestroy) m.applyDanmakuSettings(h.art, settings) })
  }, [ready, h, settings])

  const change = (patch: Partial<DanmakuSettings>) => setSettings((cur) => {
    const next = { ...cur, ...patch }
    saveDanmakuSettings(next)
    return next
  })
  const submit = async (text: string) => {
    const v = h.video
    if (!source || !(v.duration > 0) || !Number.isFinite(v.duration)) throw new Error("视频加载完成后才能发送弹幕")
    const key = latestKey.current
    const r = await api.sendDanmaku(source.provider, source.id, source.episode, { time: v.currentTime, duration: v.duration, text })
    if (key === latestKey.current && !h.art.isDestroy) emitter.current?.(r.item)
  }
  const centerActive = center && !!source && !!mediaKey
  const box = (variant: "bar" | "dialog") => source && mediaKey ? (
    <DanmakuSendBox key={sourceKey} variant={variant} canSend={send} loggedIn={!!me.data?.user}
      loginHref={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} onSend={submit} />
  ) : <p className={variant === "bar" ? "truncate text-xs text-white/70" : "text-[13px] text-muted-foreground"}>本视频尚未关联到具体集数，暂不能发送弹幕</p>

  return <>
    {centerActive && createPortal(box("bar"), h.danmakuSlot)}
    <Dialog open={requested} onOpenChange={dismiss}>
      <DialogContent container={host} title="弹幕" className="gap-4 sm:max-w-sm">
        {!centerActive && box("dialog")}
        <DanmakuSettingsForm settings={settings} onSettings={change} />
      </DialogContent>
    </Dialog>
  </>
}
