// 弹幕栏（单集页，播放器与标题之间）：「弹幕」按钮（打开显示设置）+ 发送框。全屏时不显示（设置照样生效）。
import { useEffect, useRef, useState } from "react"
import { Link } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { isApiError } from "@/lib/api"
import {
  AREA_OPTIONS,
  DANMAKU_MAX_TEXT,
  SIZE_OPTIONS,
  SPEED_OPTIONS,
  danmakuTextProblem,
  type DanmakuSettings,
} from "@/lib/danmaku"

export function DanmakuBar({
  settings,
  onSettings,
  canSend,
  loggedIn,
  loginHref,
  onSend,
}: {
  settings: DanmakuSettings
  /** 修改部分设置（调用方合并并保存） */
  onSettings: (patch: Partial<DanmakuSettings>) => void
  /** 有发送权限 */
  canSend: boolean
  loggedIn: boolean
  loginHref: string
  /** 发送（成功后由调用方显示到播放器里）；失败时抛出，消息显示在栏下方 */
  onSend: (text: string) => Promise<void>
}) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState("")
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)

  // 设置面板：点外面 / Esc 关闭
  useEffect(() => {
    if (!open) return
    const down = (e: PointerEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false)
    document.addEventListener("pointerdown", down)
    document.addEventListener("keydown", key)
    return () => {
      document.removeEventListener("pointerdown", down)
      document.removeEventListener("keydown", key)
    }
  }, [open])

  // 错误提示几秒后消失
  useEffect(() => {
    if (!error) return
    const t = window.setTimeout(() => setError(null), 4000)
    return () => window.clearTimeout(t)
  }, [error])

  const problem = danmakuTextProblem(text)
  const send = async () => {
    if (problem !== null || sending) return
    setSending(true)
    setError(null)
    try {
      await onSend(text.trim())
      setText("")
    } catch (e) {
      setError(isApiError(e) ? e.message : String((e as Error)?.message ?? e))
    } finally {
      setSending(false)
      input.current?.focus()
    }
  }
  const set = onSettings

  return (
    <div className="pt-2 lg:pt-3">
      <div className="flex items-center gap-2">
        <div ref={box} className="relative shrink-0">
          <Button
            variant="outline"
            size="sm"
            aria-expanded={open}
            aria-haspopup="dialog"
            onClick={() => setOpen((v) => !v)}
            className={cn("px-3", !settings.visible && "text-muted-foreground line-through")}
          >
            弹幕
          </Button>
          {open && (
            <div role="dialog" aria-label="弹幕设置" className="absolute left-0 top-full z-30 mt-1.5 w-64 space-y-3 rounded-md border bg-card p-3 text-[13px]">
              <Row label="显示弹幕">
                <Switch checked={settings.visible} onCheckedChange={(v) => set({ visible: v })} aria-label="显示弹幕" />
              </Row>
              <Row label="不透明度">
                <input
                  type="range"
                  min={10}
                  max={100}
                  step={5}
                  value={Math.round(settings.opacity * 100)}
                  onChange={(e) => set({ opacity: Number(e.target.value) / 100 })}
                  aria-label="不透明度"
                  className="h-1.5 w-24 cursor-pointer accent-primary"
                />
                <span className="w-9 text-right tabular-nums text-muted-foreground">{Math.round(settings.opacity * 100)}%</span>
              </Row>
              <Row label="字号">
                <Choice options={SIZE_OPTIONS} value={settings.size} onChange={(v) => set({ size: v })} />
              </Row>
              <Row label="速度">
                <Choice options={SPEED_OPTIONS} value={settings.speed} onChange={(v) => set({ speed: v })} />
              </Row>
              <Row label="显示区域">
                <Choice options={AREA_OPTIONS} value={settings.area} onChange={(v) => set({ area: v })} />
              </Row>
            </div>
          )}
        </div>
        {canSend ? (
          <form
            className="flex min-w-0 flex-1 items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void send()
            }}
          >
            <Input
              ref={input}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder="发个弹幕…"
              aria-label="弹幕内容"
              aria-invalid={!!problem || undefined}
              enterKeyHint="send"
              className="h-9 min-w-0 flex-1"
            />
            <Button type="submit" size="sm" disabled={problem !== null || sending} className="shrink-0">
              发送
            </Button>
          </form>
        ) : !loggedIn ? (
          <Link to={loginHref} className="truncate text-[13px] text-primary-text hover:underline">
            登录以使用完整功能
          </Link>
        ) : null}
      </div>
      {(problem || error) && (
        <p role="status" className="mt-1.5 text-xs text-destructive-text">
          {problem ? `${problem}（${[...text.trim()].length} / ${DANMAKU_MAX_TEXT}）` : error}
        </p>
      )}
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  )
}

function Choice<T extends string | number>({ options, value, onChange }: { options: { value: T; label: string }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="flex overflow-hidden rounded-md border">
      {options.map((o, i) => (
        <button
          key={String(o.value)}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "h-7 min-w-9 px-2 text-xs transition-colors",
            i > 0 && "border-l",
            o.value === value ? "bg-primary text-primary-foreground" : "hover:bg-muted",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
