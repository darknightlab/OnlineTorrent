import { cn } from "@/lib/utils"

export function Container({ className, children }: { className?: string; children: React.ReactNode }) {
  return <div className={cn("mx-auto w-full max-w-[1680px] px-3 sm:px-4 lg:px-[5vw]", className)}>{children}</div>
}
