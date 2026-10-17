import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react"

export type Theme = "light" | "dark" | "system"
const KEY = "pk-theme"

interface ThemeCtx {
  theme: Theme
  resolved: "light" | "dark"
  setTheme: (t: Theme) => void
}

const Ctx = createContext<ThemeCtx | null>(null)

export function useTheme() {
  const c = useContext(Ctx)
  if (!c) throw new Error("useTheme outside ThemeProvider")
  return c
}

const mq = () => window.matchMedia("(prefers-color-scheme: dark)")

function resolve(t: Theme): "light" | "dark" {
  return t === "dark" || (t === "system" && mq().matches) ? "dark" : "light"
}

function apply(r: "light" | "dark") {
  const el = document.documentElement
  el.classList.toggle("dark", r === "dark")
  el.style.colorScheme = r
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", r === "dark" ? "#121614" : "#ffffff")
}

function readStored(): Theme {
  const v = localStorage.getItem(KEY)
  return v === "light" || v === "dark" || v === "system" ? v : "system"
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(readStored)
  const [resolved, setResolved] = useState<"light" | "dark">(() => resolve(readStored()))

  useEffect(() => {
    const r = resolve(theme)
    setResolved(r)
    apply(r)
    if (theme !== "system") return
    const m = mq()
    const onChange = () => {
      const r2 = resolve("system")
      setResolved(r2)
      apply(r2)
    }
    m.addEventListener("change", onChange)
    return () => m.removeEventListener("change", onChange)
  }, [theme])

  const setTheme = useCallback((t: Theme) => {
    localStorage.setItem(KEY, t)
    setThemeState(t)
  }, [])

  return <Ctx.Provider value={{ theme, resolved, setTheme }}>{children}</Ctx.Provider>
}
