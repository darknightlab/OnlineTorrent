import { Link } from "react-router-dom"
import { Home, Search } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { Button } from "@/components/ui/button"
import { useDocumentTitle } from "@/lib/hooks"

export function StatusPage({
  code,
  title,
  description,
  children,
}: {
  code: string
  title: string
  description: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <Container className="flex min-h-[60vh] flex-col items-center justify-center py-12 text-center">
      <p className="bg-gradient-to-br from-primary to-accent bg-clip-text text-7xl font-black tracking-tighter text-transparent sm:text-8xl">{code}</p>
      <h1 className="mt-3 text-lg font-semibold sm:text-xl">{title}</h1>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">{description}</p>
      <div className="mt-6 flex flex-wrap justify-center gap-2">
        {children ?? (
          <>
            <Button asChild>
              <Link to="/">
                <Home />
                返回首页
              </Link>
            </Button>
            <Button asChild variant="outline">
              <Link to="/search">
                <Search />
                搜索种子
              </Link>
            </Button>
          </>
        )}
      </div>
    </Container>
  )
}

export default function NotFound() {
  useDocumentTitle("页面不存在")
  return <StatusPage code="404" title="页面不存在" description="你访问的页面可能已被移动或删除。" />
}
