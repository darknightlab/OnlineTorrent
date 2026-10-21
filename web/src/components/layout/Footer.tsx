import { Container } from "./Container"
import { LogoMark } from "./Logo"

export function Footer() {
  return (
    <footer className="mt-10 hidden border-t bg-card/60 sm:block">
      <Container className="flex flex-col gap-3 py-6 text-xs text-muted-foreground lg:flex-row lg:items-center lg:justify-between">
        <div className="flex shrink-0 items-center gap-2">
          <LogoMark className="h-5 w-5" />
          <span className="whitespace-nowrap">
            © 2022–{new Date().getFullYear()} Project Kawaii · projectk.org
          </span>
        </div>
        <div className="space-y-1 leading-relaxed lg:text-right">
          <p>基于 WebTorrent 在浏览器中点对点播放；观看时会同时为其他用户上传数据。本站不存储任何视频文件。</p>
          <p>
            番剧资料来自{" "}
            <a className="underline-offset-2 hover:underline" href="https://bgm.tv" target="_blank" rel="noreferrer">
              Bangumi 番组计划
            </a>
            、
            <a className="underline-offset-2 hover:underline" href="https://github.com/bangumi-data/bangumi-data" target="_blank" rel="noreferrer">
              bangumi-data
            </a>
            ；名称匹配使用{" "}
            <a className="underline-offset-2 hover:underline" href="https://github.com/manami-project/anime-offline-database" target="_blank" rel="noreferrer">
              anime-offline-database
            </a>
            （manami-project，依{" "}
            <a className="underline-offset-2 hover:underline" href="https://opendatacommons.org/licenses/odbl/1-0/" target="_blank" rel="noreferrer">
              ODbL 1.0
            </a>{" "}
            授权）。
          </p>
        </div>
      </Container>
    </footer>
  )
}
