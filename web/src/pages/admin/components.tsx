import type { ReactNode } from "react"
import { Card } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { EmptyState, ErrorState, Spinner } from "@/components/states"
import { cn } from "@/lib/utils"

export function AdminHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-end justify-between gap-3 sm:mb-5">
      <div>
        <h1 className="text-lg font-bold sm:text-xl">{title}</h1>
        {description && <p className="mt-0.5 text-[13px] text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  )
}

export interface Column<T> {
  key: string
  header: ReactNode
  cell: (row: T) => ReactNode
  className?: string
  /** 移动端卡片：title=标题行，hidden=不显示，默认作为 label: value 行 */
  mobile?: "title" | "hidden" | "default"
}

/** 桌面为表格、移动端为卡片列表的响应式数据视图 */
export function DataView<T>({
  columns,
  rows,
  rowKey,
  actions,
  loading,
  error,
  onRetry,
  empty = "暂无数据",
}: {
  columns: Column<T>[]
  rows: T[] | undefined
  rowKey: (r: T) => string | number
  actions?: (r: T) => ReactNode
  loading?: boolean
  error?: unknown
  onRetry?: () => void
  empty?: ReactNode
}) {
  if (error)
    return (
      <Card>
        <ErrorState error={error} onRetry={onRetry} />
      </Card>
    )
  if (loading || !rows)
    return (
      <Card className="flex h-40 items-center justify-center">
        <Spinner />
      </Card>
    )
  if (rows.length === 0)
    return (
      <Card>
        <EmptyState title={empty} />
      </Card>
    )
  const titleCol = columns.find((c) => c.mobile === "title") ?? columns[0]
  return (
    <>
      <Card className="hidden overflow-hidden md:block">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {columns.map((c) => (
                <TableHead key={c.key} className={c.className}>
                  {c.header}
                </TableHead>
              ))}
              {actions && <TableHead className="text-right">操作</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={rowKey(r)}>
                {columns.map((c) => (
                  <TableCell key={c.key} className={c.className}>
                    {c.cell(r)}
                  </TableCell>
                ))}
                {actions && (
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">{actions(r)}</div>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
      <ul className="grid gap-2.5 md:hidden">
        {rows.map((r) => (
          <li key={rowKey(r)}>
            <Card className="p-3.5">
              <div className="text-sm font-medium">{titleCol.cell(r)}</div>
              <dl className="mt-2 grid gap-1.5 text-[13px]">
                {columns
                  .filter((c) => c !== titleCol && c.mobile !== "hidden")
                  .map((c) => (
                    <div key={c.key} className="flex items-start justify-between gap-3">
                      <dt className="shrink-0 text-muted-foreground">{c.header}</dt>
                      <dd className="min-w-0 text-right">{c.cell(r)}</dd>
                    </div>
                  ))}
              </dl>
              {actions && <div className="mt-3 flex flex-wrap justify-end gap-2 border-t pt-3">{actions(r)}</div>}
            </Card>
          </li>
        ))}
      </ul>
    </>
  )
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("wrap-anywhere font-mono text-xs", className)}>{children}</span>
}

export function StatusDot({ ok, label }: { ok: boolean | null; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-[13px]">
      <span className={cn("h-2 w-2 rounded-full", ok === null ? "bg-muted-foreground/40" : ok ? "bg-primary" : "bg-destructive")} />
      {label}
    </span>
  )
}
