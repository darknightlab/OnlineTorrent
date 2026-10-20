import { useLayoutEffect, type RefObject } from "react"
import { snapBox } from "@/lib/pixelsnap"

/**
 * 播放框对齐到整设备像素（见 lib/pixelsnap）：outer 是占位的外框（宽度由布局决定），inner 是实际的
 * 16:9 播放框（黑底、圆角裁剪都在它上面），按 outer 的位置与宽度设置 inner 的 margin-left / width / height。
 * 窗口大小、布局、设备像素比变化时重新计算；全屏时不动（全屏下屏幕就是框）。
 */
export function useSnappedFrame(outer: RefObject<HTMLElement | null>, inner: RefObject<HTMLElement | null>) {
  useLayoutEffect(() => {
    const o = outer.current
    const i = inner.current
    if (!o || !i) return
    let raf = 0
    const apply = () => {
      raf = 0
      if (document.fullscreenElement) return
      const r = o.getBoundingClientRect()
      if (!r.width) return
      const s = snapBox(r.left, r.width, window.devicePixelRatio || 1)
      i.style.marginLeft = `${s.marginLeft}px`
      i.style.width = `${s.width}px`
      i.style.height = `${s.height}px`
    }
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(apply)
    }
    apply()
    const ro = new ResizeObserver(schedule)
    ro.observe(o)
    window.addEventListener("resize", schedule) // 位置变了而大小没变（居中容器）
    document.addEventListener("fullscreenchange", schedule)
    // 设备像素比变化（缩放、拖到另一块屏幕）
    let mq: MediaQueryList | null = null
    const onDpr = () => {
      watchDpr()
      schedule()
    }
    const watchDpr = () => {
      mq?.removeEventListener("change", onDpr)
      mq = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`)
      mq.addEventListener("change", onDpr)
    }
    watchDpr()
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      window.removeEventListener("resize", schedule)
      document.removeEventListener("fullscreenchange", schedule)
      mq?.removeEventListener("change", onDpr)
      i.style.marginLeft = i.style.width = i.style.height = ""
    }
  }, [outer, inner])
}
