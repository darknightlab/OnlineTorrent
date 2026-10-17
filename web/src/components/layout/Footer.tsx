import { Container } from "./Container"
import { LogoMark } from "./Logo"

export function Footer() {
  return (
    <footer className="mt-10 border-t bg-card/60">
      <Container className="flex flex-col gap-3 py-6 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <LogoMark className="h-5 w-5" />
          <span>
            © {new Date().getFullYear()} Project Kawaii · projectk.org
          </span>
        </div>
        <p className="leading-relaxed">
          基于 WebTorrent 在浏览器中点对点播放；观看时会同时为其他用户上传数据。本站不存储任何视频文件。
        </p>
      </Container>
    </footer>
  )
}
