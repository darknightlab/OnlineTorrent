import { Check, Monitor, Moon, Sun } from "lucide-react"
import { useTheme, type Theme } from "@/lib/theme"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"

const options: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Monitor },
]

export function ThemeToggle() {
  const { theme, resolved, setTheme } = useTheme()
  const current = options.find((o) => o.value === theme)!
  const Icon = theme === "system" ? Monitor : resolved === "dark" ? Moon : Sun
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={`切换主题（当前：${current.label}）`} className="text-muted-foreground hover:text-foreground">
          <Icon className="!size-[18px]" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-36">
        {options.map((o) => (
          <DropdownMenuItem key={o.value} onSelect={() => setTheme(o.value)}>
            <o.icon />
            <span className="flex-1">{o.label}</span>
            {theme === o.value && <Check className="!text-primary-text" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 移动端设置页用的三段式切换 */
export function ThemeSegmented() {
  const { theme, setTheme } = useTheme()
  return (
    <div role="radiogroup" aria-label="主题" className="inline-flex rounded-lg border bg-muted p-0.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={theme === o.value}
          onClick={() => setTheme(o.value)}
          className={
            "inline-flex h-9 items-center gap-1.5 rounded-md px-3 text-[13px] font-medium transition-colors " +
            (theme === o.value ? "bg-card text-foreground" : "text-muted-foreground hover:text-foreground")
          }
        >
          <o.icon className="h-4 w-4" />
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** 用户菜单里的主题选项（不关闭菜单） */
export function ThemeMenuItems() {
  const { theme, setTheme } = useTheme()
  return (
    <>
      <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">主题</DropdownMenuLabel>
      {options.map((o) => (
        <DropdownMenuItem
          key={o.value}
          onSelect={(e) => {
            e.preventDefault()
            setTheme(o.value)
          }}
        >
          <o.icon />
          <span className="flex-1">{o.label}</span>
          {theme === o.value && <Check className="!text-primary-text" />}
        </DropdownMenuItem>
      ))}
    </>
  )
}

const cycle: Theme[] = ["light", "dark", "system"]

/** 单个按钮：浅色 → 深色 → 跟随系统 循环（移动端顶栏） */
export function ThemeCycleButton() {
  const { theme, setTheme } = useTheme()
  const cur = options.find((o) => o.value === theme)!
  const next = options.find((o) => o.value === cycle[(cycle.indexOf(theme) + 1) % cycle.length])!
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setTheme(next.value)}
      aria-label={`主题：${cur.label}（点击切换为${next.label}）`}
      title={`主题：${cur.label}`}
      className="text-foreground"
    >
      <cur.icon className="!size-5" />
    </Button>
  )
}
