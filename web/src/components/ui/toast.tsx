import { useSyncExternalStore } from "react"
import { CheckCircle2, AlertCircle, Info, X } from "lucide-react"
import { cn } from "@/lib/utils"

type Kind = "success" | "error" | "info"
interface ToastItem {
  id: number
  kind: Kind
  message: string
}

let items: ToastItem[] = []
let seq = 0
const listeners = new Set<() => void>()
const emit = () => listeners.forEach((l) => l())

function push(kind: Kind, message: string, ms = 3200) {
  const id = ++seq
  items = [...items.slice(-3), { id, kind, message }]
  emit()
  window.setTimeout(() => dismiss(id), ms)
}

function dismiss(id: number) {
  items = items.filter((t) => t.id !== id)
  emit()
}

export const toast = {
  success: (m: string) => push("success", m),
  error: (m: string) => push("error", m, 5000),
  info: (m: string) => push("info", m),
}

const icons = { success: CheckCircle2, error: AlertCircle, info: Info }
const tone = { success: "text-primary", error: "text-destructive", info: "text-accent" }

export function Toaster() {
  const list = useSyncExternalStore(
    (cb) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    () => items,
  )
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed inset-x-0 bottom-[calc(4.5rem+env(safe-area-inset-bottom))] z-[60] flex flex-col items-center gap-2 px-4 sm:bottom-6 sm:right-6 sm:left-auto sm:items-end"
    >
      {list.map((t) => {
        const Icon = icons[t.kind]
        return (
          <div
            key={t.id}
            role="status"
            className="pointer-events-auto flex w-full max-w-sm items-center gap-2.5 rounded-lg border bg-popover px-3.5 py-2.5 text-sm text-popover-foreground"
          >
            <Icon className={cn("h-4 w-4 shrink-0", tone[t.kind])} />
            <span className="min-w-0 flex-1">{t.message}</span>
            <button
              type="button"
              aria-label="关闭提示"
              onClick={() => dismiss(t.id)}
              className="-mr-1 inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-muted"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        )
      })}
    </div>
  )
}
