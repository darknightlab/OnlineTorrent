// 弹幕层（artplayer-plugin-danmuku）。单独成块：只在单集页由 ArtVideo 动态加载。
// 插件自带的发送框 / 设置面板 / 开关图标都不用（artplayer.css 隐藏；开关、设置、发送在播放器下方的弹幕栏）；
// 弹幕显示在 ArtPlayer 的 .art-danmuku 层（z 30）：视频之上，内封字幕（z 35）与控制栏之下。
import type Artplayer from "artplayer"
import artplayerPluginDanmuku, { type Result } from "artplayer-plugin-danmuku"
import type { DanmakuItem } from "@/lib/types"
import { pluginOptions, toDanmu, type DanmakuSettings } from "@/lib/danmaku"

/** 每个 ArtPlayer 实例最多一个弹幕插件（同名插件不能重复添加），换集时重新载入 */
const layers = new WeakMap<Artplayer, Result>()

const isMobile = (art: Artplayer) => (art.constructor as typeof Artplayer).utils.isMobile

/** 显示一集的弹幕（items 为空 = 清空，插件仍在，可以发送） */
export function showDanmaku(art: Artplayer, items: DanmakuItem[], settings: DanmakuSettings) {
  let plugin = layers.get(art)
  if (!plugin) {
    art.plugins.add(
      artplayerPluginDanmuku({
        danmuku: toDanmu(items),
        emitter: false,
        heatmap: false,
        antiOverlap: true,
        // 窄播放器时插件会把（隐藏的）设置栏移到播放器下方并给播放器加外边距：不让它移动
        width: 0,
        ...pluginOptions(settings, isMobile(art)),
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
  applyDanmakuSettings(art, settings)
}

/** 显示设置（开关、不透明度、字号、速度、区域）立即生效 */
export function applyDanmakuSettings(art: Artplayer, settings: DanmakuSettings) {
  const plugin = layers.get(art)
  if (!plugin || art.isDestroy) return
  // config() 合并到现有选项（类型要求 danmuku，实际可省略）
  plugin.config(pluginOptions(settings, isMobile(art)) as Parameters<Result["config"]>[0])
}

/** 自己刚发送的弹幕：马上出现（带边框）。插件只显示时间在当前位置 ±0.1 秒内的弹幕，发送请求往返之后
 * 原来的时间已经过去，所以不带时间：插件按当前位置 + 0.5 秒排入（暂停时在继续播放后出现） */
export function emitDanmaku(art: Artplayer, item: DanmakuItem) {
  const plugin = layers.get(art)
  if (!plugin || art.isDestroy) return
  const [d] = toDanmu([item])
  if (!d) return
  const { time: _sent, ...rest } = d
  void _sent
  plugin.emit({ ...rest, border: true })
}

/** 清空弹幕（换集 / 离开单集页）；没有弹幕层时什么也不做 */
export function clearDanmaku(art: Artplayer) {
  const plugin = layers.get(art)
  if (!plugin || art.isDestroy) return
  plugin.config({ danmuku: [] })
  void plugin.load()
}
