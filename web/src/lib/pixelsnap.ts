// 把播放框对齐到整设备像素。
//
// 播放框的位置 / 宽度是小数像素时（窗口宽度、侧栏、缩放比例都会造成），视频图层对齐到整设备像素，
// 而框的边缘不在像素格上：边上那一列设备像素一部分露出黑底，看起来是一条细黑线（只在某些窗口宽度出现）。
// 做法：左边缘取向上的整设备像素、右边缘取向下的整设备像素（两侧各损失不到 1 个设备像素），
// 高度按 16:9 由设备像素宽度向上取整（向下取整会让框略宽于 16:9，contain 在两侧留下亚像素黑边）。视频本身不变（object-fit: contain）。

export interface SnappedBox {
  /** 相对容器左边缘的偏移（CSS px） */
  marginLeft: number
  width: number
  height: number
}

/** left / width：容器的位置与宽度（CSS px，getBoundingClientRect）；ratio：宽高比 */
export function snapBox(left: number, width: number, dpr: number, ratio = 16 / 9): SnappedBox {
  const eps = 1e-6
  const l = Math.ceil(left * dpr - eps)
  const r = Math.floor((left + width) * dpr + eps)
  const wDev = Math.max(0, r - l)
  // 高度向上取整：框比 16:9 略窄（不会略宽），contain 时视频铺满宽度，余下不到 1 个设备像素在上下
  const hDev = Math.ceil(wDev / ratio - eps)
  return { marginLeft: l / dpr - left, width: wDev / dpr, height: hDev / dpr }
}
