// 弹幕层（artplayer-plugin-danmuku）。单独成块：只有单集页、有弹幕时才由 ArtVideo 动态加载。
// 插件自带的发送框 / 设置面板 / 开关图标都不用（artplayer.css 隐藏，控制栏只有本站的「弹幕」按钮）；
// 弹幕显示在 ArtPlayer 的 .art-danmuku 层（z 30）：视频之上，内封字幕（z 35）与控制栏之下。
import type Artplayer from "artplayer"
import artplayerPluginDanmuku, { type Result } from "artplayer-plugin-danmuku"
import type { DanmakuItem } from "@/lib/types"
import { toDanmu } from "@/lib/danmaku"

/** 每个 ArtPlayer 实例最多一个弹幕插件（同名插件不能重复添加），换集时重新载入 */
const layers = new WeakMap<Artplayer, Result>()

/** 显示一集的弹幕（items 为空 = 清空） */
export function showDanmaku(art: Artplayer, items: DanmakuItem[], visible: boolean) {
  let plugin = layers.get(art)
  if (!plugin) {
    const mobile = (art.constructor as typeof Artplayer).utils.isMobile
    art.plugins.add(
      artplayerPluginDanmuku({
        danmuku: toDanmu(items),
        emitter: false,
        heatmap: false,
        visible,
        antiOverlap: true,
        fontSize: mobile ? 15 : 24,
        // 窄播放器时插件会把（隐藏的）设置栏移到播放器下方并给播放器加外边距：不让它移动
        width: 0,
      }),
    )
    plugin = art.plugins.artplayerPluginDanmuku as Result
    layers.set(art, plugin)
    // 插件把控制栏中部设为 flex（放它的设置栏）；本站不用中部，恢复原样
    art.template.$controlsCenter.style.display = ""
    return
  }
  // 不带参数的 load() 会清空队列并载入 option.danmuku（带参数是追加）
  plugin.config({ danmuku: toDanmu(items) })
  void plugin.load()
  setDanmakuVisible(art, visible)
}

/** 清空弹幕（换集 / 离开单集页）；没有弹幕层时什么也不做 */
export function clearDanmaku(art: Artplayer) {
  const plugin = layers.get(art)
  if (!plugin || art.isDestroy) return
  plugin.hide()
  plugin.config({ danmuku: [] })
  void plugin.load()
}

export function setDanmakuVisible(art: Artplayer, on: boolean) {
  const plugin = layers.get(art)
  if (!plugin) return
  if (on) plugin.show()
  else plugin.hide()
}
