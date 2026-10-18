import { Navigate, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { SearchX, Search as SearchIcon } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { TorrentList, TorrentListSkeleton, TorrentTableHead } from "@/components/TorrentList"
import { EmptyState, ErrorState, Pager } from "@/components/states"
import { Card } from "@/components/ui/card"
import { useSearch } from "@/lib/queries"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { searchHref } from "@/lib/utils"

const SUGGEST = ["葬送的芙莉莲", "1080p 简繁内封", "ANi", "桜都字幕组", "药屋少女的呢喃"]

/** /search?q=x（旧链接）→ /search/x；/search 无关键词显示空状态 */
export function LegacySearchPage() {
  const [sp] = useSearchParams()
  const q = sp.get("q")?.trim()
  if (q) return <Navigate replace to={searchHref(q, Number(sp.get("page")) || undefined)} />
  return <SearchPage />
}

/** /search/:keyword（翻页 ?page=） */
export default function SearchPage() {
  const { keyword = "" } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const q = keyword.trim()
  const page = Math.max(1, Number(sp.get("page")) || 1)
  const now = useNow()
  const { data, isPending, error, refetch, isFetching } = useSearch(q, page)
  useDocumentTitle(q ? `搜索：${q}` : "搜索")

  return (
    <Container className="py-4 sm:py-6">
      <div className="mx-auto max-w-3xl">
        <TorrentSearchBox key={q} defaultValue={q} autoFocus={!q} placeholder="输入关键词搜索种子，或粘贴磁力链接" />
        {!q && (
          <div className="mt-4 flex flex-wrap items-center gap-2 text-[13px]">
            <span className="text-muted-foreground">试试：</span>
            {SUGGEST.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => navigate(searchHref(s))}
                className="inline-flex h-8 items-center rounded-full border bg-card px-3 text-foreground/80 transition-colors hover:border-primary hover:text-primary-text"
              >
                {s}
              </button>
            ))}
          </div>
        )}
      </div>

      {q && (
        <section className="mt-5" aria-live="polite">
          <h1 className="mb-3 text-sm text-muted-foreground">
            「<span className="font-medium text-foreground">{q}</span>」的搜索结果
            {data && data.page_count > 1 && <span> · 共 {data.page_count} 页</span>}
          </h1>
          <Card className="overflow-hidden">
            <TorrentTableHead />
            {isPending ? (
              <TorrentListSkeleton rows={10} />
            ) : error ? (
              <ErrorState error={error} onRetry={() => refetch()} />
            ) : data.items.length ? (
              <div className={isFetching ? "opacity-60 transition-opacity" : ""}>
                <TorrentList items={data.items} now={now} />
              </div>
            ) : (
              <EmptyState icon={SearchX} title="没有找到相关种子" description="换个关键词试试，或直接粘贴磁力链接播放。" />
            )}
          </Card>
          {data && <Pager page={data.page} pageCount={data.page_count} onChange={(p) => setSp(p > 1 ? { page: String(p) } : {})} />}
        </section>
      )}
      {!q && <EmptyState icon={SearchIcon} className="mt-8" title="搜索 bangumi.moe 上的种子" description="支持番剧名、字幕组、分辨率等关键词，多个关键词用空格分隔。" />}
    </Container>
  )
}
