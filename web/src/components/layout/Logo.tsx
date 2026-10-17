import { Link } from "react-router-dom"
import { cn } from "@/lib/utils"

export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" aria-hidden className={cn("h-8 w-8 shrink-0", className)}>
      <rect width="64" height="64" rx="16" fill="#4caf50" />
      <path d="M25 19.5v25a2 2 0 0 0 3 1.7l20-12.5a2 2 0 0 0 0-3.4l-20-12.5a2 2 0 0 0-3 1.7z" fill="#fff" />
      <circle cx="48" cy="48" r="8" fill="#2196f3" stroke="currentColor" className="text-card" strokeWidth="3" />
    </svg>
  )
}

export function Logo({ compact = false }: { compact?: boolean }) {
  return (
    <Link to="/" className="flex shrink-0 items-center gap-2 rounded-md" aria-label="Project Kawaii 首页">
      <LogoMark />
      {!compact && (
        <span className="flex flex-col leading-none">
          <span className="text-[15px] font-bold tracking-tight">
            Project <span className="text-primary-text">Kawaii</span>
          </span>
          <span className="mt-0.5 text-[10px] font-medium tracking-wide text-muted-foreground">projectk.org</span>
        </span>
      )}
    </Link>
  )
}
