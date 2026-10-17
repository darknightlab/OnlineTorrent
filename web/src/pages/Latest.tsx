import { useSearchParams } from "react-router-dom"
import { Flame } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentList, TorrentListSkeleton, TorrentTableHead } from "@/components/TorrentList"
import { EmptyState, ErrorState, Pager, SectionHeader } from "@/components/states"
import { Card } from "@/components/ui/card"
import { useLatest } from "@/lib/queries"
import { useDocumentTitle, useNow } from "@/lib/hooks"

export default function LatestPage() {
  const [sp, setSp] = useSearchParams()
  const page = Math.max(1, Number(sp.get("page")) || 1)
  const now = useNow()
  const { data, isPending, error, refetch, isFetching } = useLatest(page)
  useDocumentTitle("最新种子")
  return (
    <Container className="py-4 sm:py-6">
      <SectionHeader icon={Flame} title="最新种子" extra={<span className="text-xs text-muted-foreground">来自 bangumi.moe · 每页 {data?.items.length ?? "-"} 条</span>} />
      <Card className="overflow-hidden">
        <TorrentTableHead />
        {isPending ? (
          <TorrentListSkeleton rows={14} />
        ) : error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : data.items.length ? (
          <div className={isFetching ? "opacity-60 transition-opacity" : ""}>
            <TorrentList items={data.items} now={now} />
          </div>
        ) : (
          <EmptyState title="暂无种子" />
        )}
      </Card>
      {data && (
        <Pager
          page={data.page}
          pageCount={data.page_count}
          onChange={(p) => {
            setSp({ page: String(p) })
            window.scrollTo({ top: 0, behavior: "smooth" })
          }}
        />
      )}
    </Container>
  )
}
