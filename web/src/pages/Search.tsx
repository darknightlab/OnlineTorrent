import { useState } from "react"
import { Link, Navigate, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { ChevronDown, Loader2, Magnet, SearchX, Search as SearchIcon, Tv } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { TorrentList, TorrentListSkeleton } from "@/components/TorrentList"
import { Cover } from "@/components/Cover"
import { EmptyState, ErrorState, Pager, errorMessage } from "@/components/states"
import { toast } from "@/components/ui/toast"
import { api } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton, SkeletonLine } from "@/components/ui/skeleton"
import { useSearch } from "@/lib/queries"
import { displayTitle } from "@/lib/schedule"
import type { Subject } from "@/lib/types"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { cn, searchHref, subjectHref } from "@/lib/utils"

const SUGGEST = ["葬送的芙莉莲", "1080p 简繁内封", "ANi", "桜都字幕组", "药屋少女的呢喃"]

const TYPE_LABEL: Record<string, string> = { tv: "TV", web: "WEB", movie: "剧场版", ova: "OVA", oad: "OAD", special: "特别篇" }

/** /search?q=x（旧链接）→ /search/x；/search 无关键词显示空状态 */
export function LegacySearchPage() {
  const [sp] = useSearchParams()
  const q = sp.get("q")?.trim()
  if (q) return <Navigate replace to={searchHref(q, Number(sp.get("page")) || undefined)} />
  return <SearchPage />
}

/** /search/:keyword（翻页 ?page=）：番剧卡片（第 1 页）+ 种子列表 */
export default function SearchPage() {
  const { keyword = "" } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const q = keyword.trim()
  const page = Math.max(1, Number(sp.get("page")) || 1)
  const now = useNow()
  const { data, isPending, error, refetch, isFetching, isPlaceholderData } = useSearch(q, page)
  useDocumentTitle(q ? `搜索：${q}` : "搜索")

  const subjects = data?.subjects ?? []
  const torrents = data?.torrents ?? []
  const nothing = !!data && subjects.length === 0 && torrents.length === 0

  return (
    <Container className="pb-4 pt-safe sm:py-6">
      {/* 手机：与首页顶栏里的搜索入口同一位置（安全区 + 12px），点进来框不跳动 */}
      <div className="h-3 sm:hidden" aria-hidden />
      <div className="mx-auto max-w-3xl">
        <TorrentSearchBox key={q} size="sm" defaultValue={q} autoFocus={!q} />
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
        <div className="mt-5" aria-live="polite" aria-busy={isPending || isFetching}>
          <h1 className="mb-3 text-sm text-muted-foreground">
            「<span className="font-medium text-foreground">{q}</span>」的搜索结果
          </h1>
          {isPending ? (
            <SearchSkeleton />
          ) : error ? (
            <Card>
              <ErrorState error={error} onRetry={() => refetch()} />
            </Card>
          ) : nothing ? (
            <Card>
              <EmptyState
                icon={SearchX}
                title="没有找到相关番剧或种子"
                description="换个关键词试试（中文名、原名、罗马音或字幕组都可以），或直接粘贴磁力链接播放。"
                action={
                  <Button asChild variant="soft" size="sm">
                    <Link to="/watch">
                      <Magnet />
                      粘贴磁力链接
                    </Link>
                  </Button>
                }
              />
            </Card>
          ) : (
            <div className={cn("space-y-6 transition-opacity", isFetching && isPlaceholderData && "opacity-60")}>
              {subjects.length > 0 && <SubjectSection key={q} q={q} subjects={subjects} total={data?.subjects_total ?? subjects.length} />}
              <section aria-labelledby="search-torrents">
                <SectionTitle id="search-torrents" title="种子" extra={data.total > 0 ? `共 ${data.total} 个` : undefined} />
                <Card className="overflow-hidden">
                  {torrents.length ? (
                    <>
                      <TorrentList items={torrents} now={now} />
                    </>
                  ) : (
                    <EmptyState icon={SearchX} className="py-8" title="暂无相关种子" description="可以先进入番剧页看看，字幕组发布后会自动收录。" />
                  )}
                </Card>
                <Pager page={data.page} pageCount={data.page_count} onChange={(p) => setSp(p > 1 ? { page: String(p) } : {})} />
              </section>
            </div>
          )}
        </div>
      )}
      {!q && <EmptyState icon={SearchIcon} className="mt-8" title="搜索番剧与种子" description="支持番剧名、字幕组、分辨率等关键词，多个关键词用空格分隔。" />}
    </Container>
  )
}

function SectionTitle({ id, title, extra }: { id: string; title: string; extra?: string }) {
  return (
    <div className="mb-2.5 flex items-baseline justify-between gap-3">
      <h2 id={id} className="text-[15px] font-semibold">
        {title}
      </h2>
      {extra && <span className="text-xs tabular-nums text-muted-foreground">{extra}</span>}
    </div>
  )
}

/** 番剧：移动端单行横向滚动；桌面端网格。「展开更多」每次再取一批（数量同首批，后台设置） */
function SubjectSection({ q, subjects, total }: { q: string; subjects: Subject[]; total: number }) {
  const [extra, setExtra] = useState<Subject[]>([])
  const [loading, setLoading] = useState(false)
  const shown = [...subjects, ...extra]
  const more = async () => {
    setLoading(true)
    try {
      const r = await api.searchSubjects(q, shown.length)
      setExtra((x) => [...x, ...r.subjects])
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }
  return (
    <section aria-labelledby="search-subjects">
      <SectionTitle id="search-subjects" title="番剧" extra={`${total} 部`} />
      <ul
        className={cn(
          "-mx-3 flex snap-x gap-3 overflow-x-auto px-3 pb-1 scrollbar-none",
          "sm:mx-0 sm:grid sm:snap-none sm:grid-cols-5 sm:gap-4 sm:overflow-visible sm:px-0 sm:pb-0 md:grid-cols-6 lg:grid-cols-8",
        )}
      >
        {shown.map((s) => (
          <li key={s.subject_id || s.key} className="w-[28vw] max-w-36 shrink-0 snap-start sm:w-auto sm:max-w-none">
            <SubjectResult s={s} />
          </li>
        ))}
      </ul>
      {shown.length < total && (
        <div className="mt-3 flex justify-center">
          <Button variant="ghost" size="sm" onClick={() => void more()} disabled={loading}>
            {loading ? <Loader2 className="animate-spin" /> : <ChevronDown />}
            展开更多
          </Button>
        </div>
      )}
    </section>
  )
}

function subjectMeta(s: Subject) {
  const year = s.begin ? new Date(s.begin).getFullYear() : NaN
  const type = s.type ? (TYPE_LABEL[s.type.toLowerCase()] ?? s.type.toUpperCase()) : ""
  return [Number.isFinite(year) && year > 1900 ? `${year}` : "", type].filter(Boolean).join(" · ")
}

function SubjectResult({ s }: { s: Subject }) {
  const title = displayTitle(s)
  const meta = subjectMeta(s)
  return (
    <Link
      to={subjectHref(s)}
      className="group block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      title={s.title !== title ? `${title}\n${s.title}` : title}
    >
      <div className="relative overflow-hidden rounded-lg ring-1 ring-black/5 dark:ring-white/5">
        <Cover src={s.cover} title={title} className="aspect-[3/4] w-full transition-transform duration-300 group-hover:scale-[1.03]" />
        {(s.latest_episode ?? 0) > 0 && (
          <span className="absolute bottom-1 left-1 inline-flex items-center gap-1 rounded bg-black/65 px-1.5 text-[11px] font-medium leading-[18px] text-white">
            <Tv className="h-3 w-3" />
            {s.latest_episode} 话
          </span>
        )}
      </div>
      <h3 className="mt-1.5 line-clamp-2 text-[13px] font-medium leading-snug group-hover:text-primary-text sm:text-sm">{title}</h3>
      {s.title_cn && s.title !== s.title_cn && <p className="mt-0.5 hidden truncate text-[11px] text-muted-foreground sm:block">{s.title}</p>}
      {meta && <p className="mt-0.5 text-[11px] tabular-nums text-muted-foreground">{meta}</p>}
    </Link>
  )
}

/** 与结果同样的两个区块：番剧（手机横滑、桌面网格）+ 种子列表 */
function SearchSkeleton() {
  const title = <SkeletonLine className="mb-2.5 text-[15px]" bar="w-12" />
  return (
    <div className="space-y-6" aria-hidden>
      <div>
        {title}
        <div className="-mx-3 flex gap-3 overflow-hidden px-3 pb-1 sm:mx-0 sm:grid sm:grid-cols-5 sm:gap-4 sm:px-0 sm:pb-0 md:grid-cols-6 lg:grid-cols-8">
          {Array.from({ length: 8 }, (_, i) => (
            <div key={i} className={cn("w-[28vw] max-w-36 shrink-0 sm:w-auto sm:max-w-none", i === 5 && "sm:hidden md:block", i >= 6 && "sm:hidden lg:block")}>
              <Skeleton className="aspect-[3/4] w-full rounded-lg" />
              <SkeletonLine className="mt-1.5 text-[13px] leading-snug sm:text-sm" bar="w-4/5" />
              <SkeletonLine className="mt-0.5 text-[11px]" bar="w-1/2" />
            </div>
          ))}
        </div>
      </div>
      <div>
        {title}
        <Card className="overflow-hidden">
          <TorrentListSkeleton rows={8} />
        </Card>
      </div>
    </div>
  )
}
