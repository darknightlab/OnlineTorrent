import { lazy, Suspense } from "react"
import { cn } from "@/lib/utils"

const Impl = lazy(() => import("./MarkdownImpl"))

/** Markdown 渲染（react-markdown 懒加载，避免进入首屏主包） */
export function Markdown({ children, className }: { children: string; className?: string }) {
  return (
    <div className={cn("prose-kawaii", className)}>
      <Suspense fallback={<p className="whitespace-pre-wrap text-muted-foreground">{children}</p>}>
        <Impl>{children}</Impl>
      </Suspense>
    </div>
  )
}
