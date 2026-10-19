# onlinetorrent v2 前端（web/）

React 19 + TypeScript + Vite 8 + Tailwind CSS v4 + Radix（shadcn 风格组件）+ TanStack Query + React Router v7 + webtorrent@3。
构建产物输出到 `web/dist/`，由 Go 服务 embed（`dist/.keep` 会在构建后自动保留）。

## 运行

```bash
cd web
npm install            # 环境里 NODE_ENV=production 时，.npmrc 的 include=dev 保证安装 vite/tsc
npm run dev            # 代理 /api、/meta → http://127.0.0.1:8080（VITE_API_TARGET 可覆盖）
npm run dev:mock       # VITE_MOCK=1：拦截 /api/v1/*，使用 src/mocks 的假数据 + 模拟播放引擎
npm run build          # tsc -b && vite build → dist/
```

Mock 模式下的 URL 参数：
- `?mock_user=guest|member|vip|admin`：切换身份（存 sessionStorage，默认 admin）
- `?real=1`：播放页改用真实 WebTorrent 引擎（play 接口仍是 mock）

任意页面都支持 `?theme=light|dark|system`，写入 localStorage（`pk-theme`）。

## 结构

```
src/
  lib/        api.ts（类型化客户端，ApiError{status,code}）、types.ts（docs/api.md 对应类型）、
              queries.ts（TanStack Query hooks）、theme.tsx、format.ts、schedule.ts、utils.ts、hooks.ts、
              pieces.ts（进度条 piece 图层）
  components/ ui/（button/badge/card/input/dialog/sheet/tabs/dropdown/table/toast…）、layout/（Header、
              MobileTabBar、Footer、Logo、ThemeToggle）、SubjectCard、TorrentList、Cover、Markdown（懒加载）、Turnstile
  pages/      Home、Search、Subject（条目）、Episode（单集，懒加载）、Auth（登录/注册）、Me、NotFound、
              watch/（Watch、Playback、Player、ArtVideo + artplayer-setup（ArtPlayer，单独 chunk）、PlayGate、ReportDialog、
              usePlayResolve、useStallWatchdog）—— 懒加载
              admin/（AdminApp + 概览/设置/qB/用户/邀请码兑换码/规则/审核/播放记录/审计）—— 懒加载
  player/     引擎抽象（types.ts）、webtorrent-engine.ts（SW 流式、首尾 piece 优先）、segments.ts（200 段进度）
  mocks/      仅 VITE_MOCK=1 时动态 import；seed.json 为 bgm.tv/bangumi.moe 真实条目快照
```

- `/sw.min.js` 由 vite 插件从 `node_modules/webtorrent/dist/sw.min.js` 提供（dev 中间件 + build 产出），scope `/`；
  SW 拦截 `/webtorrent/*`，Go 服务不要占用该路径，并需对 SPA 路由回退到 index.html。
- 页面路径见 docs/api.md「路径约定」；链接统一用 `lib/utils.ts` 的 `watchHref` / `subjectHref` / `episodeHref` / `searchHref` 生成。
  条目链接有 bgm id 时用 `/subject/bgm/{id}`，否则 `/subject/id/{subject_id}`。
- 旧链接兼容（前端重定向）：`/?uri=磁力`、`/watch?uri=|ih=` → `/watch/hash/{ih}`；`/watch?t={moe id}` 先调
  `POST /torrents/moe/{id}/play`，成功后替换为 `/watch/hash/{ih}` 并复用这次结果（不重复请求）；
  `/anime/bgm:N` → `/subject/bgm/N`；`/search?q=x` → `/search/x`；`/register?invite=X` → `/register/invite/X`。
- `/watch/hash/{ih}` 与 `/watch/hash/{ih}/file/{index}` 是同一个路由：文件列表里切换文件只替换 URL，不重建播放器。
- 单集页 `/subject/{provider}/{id}/episode/{n}` 自动播放 `items[0]`，「其他资源」切换只换播放源，URL 不变。
- play 网络错误或 5xx：5 秒后回退为直连磁力（URL 中的 infohash，或列表跳转时携带的 infohash）。
