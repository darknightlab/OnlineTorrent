import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import App from "./App"
import "./index.css"

// 构建标识（VITE_BUILD_ID，可选）：排查 PWA 正在运行哪个版本；e2e 用它构造「新版本」
if (import.meta.env.VITE_BUILD_ID) document.documentElement.dataset.build = import.meta.env.VITE_BUILD_ID

async function boot() {
  // mock 仅在 VITE_MOCK=1 时动态引入；生产构建中该分支被静态消除，不会打包 fixtures。
  if (import.meta.env.VITE_MOCK === "1") {
    const { installMock } = await import("./mocks")
    installMock()
  }
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}

void boot()
