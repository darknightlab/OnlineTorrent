import * as React from "react"
import * as DialogPrimitive from "@radix-ui/react-dialog"
import { X } from "lucide-react"
import { cn } from "@/lib/utils"

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger
export const DialogClose = DialogPrimitive.Close

const overlay =
  "fixed inset-0 z-50 bg-black/45 backdrop-blur-[2px]"

export function DialogContent({
  className,
  children,
  title,
  description,
  container,
  ...props
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & {
  title: React.ReactNode
  description?: React.ReactNode
  /** 挂载位置（默认 body）；全屏元素里弹窗时传全屏元素，否则会被挡住 */
  container?: HTMLElement | null
}) {
  return (
    <DialogPrimitive.Portal container={container ?? undefined}>
      {/* 挂在播放器（全屏 / 网页全屏）里时要高于播放器自己的图层（控制栏 60 … 右键菜单 120） */}
      <DialogPrimitive.Overlay className={cn(overlay, container && "z-[200]")} />
      <DialogPrimitive.Content
        className={cn(
          "fixed z-50 grid",
          container && "z-[200]",
          "max-h-[92dvh] w-full gap-4 overflow-y-auto border bg-popover p-5 text-popover-foreground focus:outline-none",
          // 手机：底部弹出；桌面：居中
          "inset-x-0 bottom-0 rounded-t-xl pb-[max(1.25rem,env(safe-area-inset-bottom))] sm:inset-auto sm:left-1/2 sm:top-1/2 sm:max-w-lg sm:-translate-x-1/2 sm:-translate-y-1/2 sm:rounded-xl sm:pb-5",
          className,
        )}
        {...props}
      >
        <div className="pr-8">
          <DialogPrimitive.Title className="text-base font-semibold">{title}</DialogPrimitive.Title>
          {description ? (
            <DialogPrimitive.Description className="mt-1 text-[13px] text-muted-foreground">
              {description}
            </DialogPrimitive.Description>
          ) : (
            <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
          )}
        </div>
        {children}
        <DialogPrimitive.Close
          className="absolute right-3 top-3 inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label="关闭"
        >
          <X className="h-4 w-4" />
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  )
}

export function DialogFooter({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)} {...props} />
}

/** 侧边抽屉（移动端导航） */
export function SheetContent({
  className,
  children,
  title,
  side = "left",
  ...props
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & { title: string; side?: "left" | "right" }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className={overlay} />
      <DialogPrimitive.Content
        className={cn(
          "fixed inset-y-0 z-50 flex w-[82vw] max-w-72 flex-col border-r bg-card pt-safe pb-safe focus:outline-none",
          side === "left" ? "left-0" : "right-0 border-l border-r-0",
          className,
        )}
        {...props}
      >
        <DialogPrimitive.Title className="sr-only">{title}</DialogPrimitive.Title>
        <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  )
}
