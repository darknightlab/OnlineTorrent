// 弹幕的设置与发送框：「弹幕」弹窗（控制栏「弹幕」按钮打开，同「资源」弹窗）里的设置 + 发送框；
// 横屏全屏时发送框改在控制栏中间（DanmakuSendBox variant="bar"，见 ArtVideo），弹窗里只有设置。
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

/** 显示设置：显示 / 不透明度 / 字号 / 速度 / 显示区域（修改立即生效，调用方合并保存） */
export function DanmakuSettingsForm({ settings, onSettings }: { settings: DanmakuSettings; onSettings: (patch: Partial<DanmakuSettings>) => void }) {
  return (
    <div className="space-y-3 text-[13px]">
      <Row label="显示弹幕">
        <Switch checked={settings.visible} onCheckedChange={(v) => onSettings({ visible: v })} aria-label="显示弹幕" />
      </Row>
      <Row label="不透明度">
        <input
          type="range"
          min={10}
          max={100}
          step={5}
          value={Math.round(settings.opacity * 100)}
          onChange={(e) => onSettings({ opacity: Number(e.target.value) / 100 })}
          aria-label="不透明度"
          className="h-1.5 w-28 cursor-pointer accent-primary"
        />
        <span className="w-9 text-right tabular-nums text-muted-foreground">{Math.round(settings.opacity * 100)}%</span>
      </Row>
      <Row label="字号">
        <Choice options={SIZE_OPTIONS} value={settings.size} onChange={(v) => onSettings({ size: v })} />
      </Row>
      <Row label="速度">
        <Choice options={SPEED_OPTIONS} value={settings.speed} onChange={(v) => onSettings({ speed: v })} />
      </Row>
      <Row label="显示区域">
        <Choice options={AREA_OPTIONS} value={settings.area} onChange={(v) => onSettings({ area: v })} />
      </Row>
    </div>
  )
}

/**
 * 发送框：输入 + 发送（回车发送，空 / 超长禁用，错误在旁边显示几秒）；游客显示「登录以使用完整功能」，
 * 登录了但没有发送权限时不显示。variant="bar"：播放器控制栏中间（暗色、紧凑，太窄时隐藏）。
 */
export function DanmakuSendBox({
  canSend,
  loggedIn,
  loginHref,
  onSend,
  variant = "dialog",
  autoFocus,
}: {
  canSend: boolean
  loggedIn: boolean
  loginHref: string
  /** 发送（成功后由调用方显示到播放器里）；失败时抛出，消息显示在发送框旁 */
  onSend: (text: string) => Promise<void>
  variant?: "dialog" | "bar"
  autoFocus?: boolean
}) {
  const [text, setText] = useState("")
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const bar = variant === "bar"

  // 错误提示几秒后消失
  useEffect(() => {
    if (!error) return
    const t = window.setTimeout(() => setError(null), 4000)
    return () => window.clearTimeout(t)
  }, [error])

  if (!canSend) {
    if (loggedIn) return null
    return (
      <Link to={loginHref} className={cn("truncate text-[13px] hover:underline", bar ? "text-white/85" : "text-primary-text")}>
        登录以使用完整功能
      </Link>
    )
  }

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
  const message = problem ? `${problem}（${[...text.trim()].length} / ${DANMAKU_MAX_TEXT}）` : error

  return (
    <div className={cn("relative min-w-0", bar ? "w-full" : "space-y-1.5")}>
      <form
        className="flex min-w-0 items-center gap-2"
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
          autoFocus={autoFocus}
          className={cn(
            "min-w-0 flex-1",
            bar ? "h-8 border-white/25 bg-white/15 text-[13px] text-white placeholder:text-white/60 focus-visible:border-white/60 focus-visible:ring-0" : "h-9",
          )}
        />
        <Button type="submit" size={bar ? "xs" : "sm"} disabled={problem !== null || sending} className={cn("shrink-0", bar && "h-8 px-3")}>
          发送
        </Button>
      </form>
      {message && (
        <p
          role="status"
          className={cn("text-xs", bar ? "absolute bottom-full left-0 mb-1.5 max-w-full truncate rounded bg-black/75 px-2 py-1 text-red-300" : "text-destructive-text")}
        >
          {message}
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
            "h-7 min-w-10 px-2 text-xs transition-colors",
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
