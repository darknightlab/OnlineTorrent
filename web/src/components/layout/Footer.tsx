import { Container } from "./Container"
import { LogoMark } from "./Logo"

export function Footer() {
  return (
    <footer className="mt-10 hidden border-t bg-card/60 md:block">
      <Container className="flex flex-col gap-3 py-6 text-xs text-muted-foreground lg:flex-row lg:items-center lg:justify-between">
        <div className="flex shrink-0 items-center gap-2">
          <LogoMark className="h-5 w-5" />
          <span className="whitespace-nowrap">
            © 2022–{new Date().getFullYear()} Project Kawaii · projectk.org
          </span>
        </div>
        <p className="leading-relaxed lg:text-right">
          基于 WebTorrent 在浏览器中点对点播放；观看时会同时为其他用户上传数据。本站不存储任何视频文件。
        </p>
      </Container>
    </footer>
  )
}
