// 内容范围的标签表达式（与后端 internal/store/content_scope.go 语义一致）：
//   "t0 op0 t1 op1 t2 …"，AND 优先于 OR —— 在每个 OR 处断开成若干组，条目只要拥有某一组的全部标签即匹配。
//   例：原创 AND 校园 OR 战斗 = (原创 ∧ 校园) ∨ 战斗。
// exclude_tags：拥有其中任意标签的条目隐藏（「全部」模式也生效）。
// 本文件不依赖其他模块（node --test 直接运行 scope.test.ts）。

export type TagOperatorValue = "and" | "or"

export interface ScopeLike {
  tag_mode: "all" | "selected"
  tags: string[]
  tag_operators: TagOperatorValue[]
  exclude_tags: string[]
}

export const MAX_SCOPE_TAGS = 50
export const MAX_SCOPE_TAG_CHARS = 60

/** 按 OR 断开的 AND 组（组内去重；空白标签忽略） */
export function tagGroups(tags: string[], operators: TagOperatorValue[]): string[][] {
  const groups: string[][] = []
  let cur: string[] = []
  tags.forEach((raw, i) => {
    const tag = raw.trim()
    if (i > 0 && (operators[i - 1] ?? "and") === "or" && cur.length) {
      groups.push([...new Set(cur)])
      cur = []
    }
    if (tag) cur.push(tag)
  })
  if (cur.length) groups.push([...new Set(cur)])
  return groups
}

/** 预览字符串："(原创 AND 校园) OR 战斗" */
export function expressionString(tags: string[], operators: TagOperatorValue[]): string {
  const groups = tagGroups(tags, operators)
  return groups.map((g) => (g.length > 1 && groups.length > 1 ? `(${g.join(" AND ")})` : g.join(" AND "))).join(" OR ")
}

/** 条目（标签并集）是否在范围内 */
export function scopeVisible(scope: ScopeLike | null | undefined, subjectTags: Iterable<string>): boolean {
  if (!scope) return true
  const set = new Set(subjectTags)
  if (scope.exclude_tags.some((t) => set.has(t))) return false
  if (scope.tag_mode === "all") return true
  return tagGroups(scope.tags, scope.tag_operators).some((g) => g.every((t) => set.has(t)))
}

/** 与后端相同的输入校验；返回错误信息或 null */
export function validateScope(scope: ScopeLike): string | null {
  if (scope.tag_mode !== "all" && scope.tag_mode !== "selected") return "标签模式无效"
  if (scope.tags.length > MAX_SCOPE_TAGS || scope.exclude_tags.length > MAX_SCOPE_TAGS) return `每个列表最多 ${MAX_SCOPE_TAGS} 个标签`
  for (const t of [...scope.tags, ...scope.exclude_tags]) {
    if (!t.trim()) return "标签不能为空"
    if ([...t.trim()].length > MAX_SCOPE_TAG_CHARS) return `标签过长（最多 ${MAX_SCOPE_TAG_CHARS} 字）：${t}`
  }
  if (scope.tag_operators.length !== Math.max(0, scope.tags.length - 1)) return "运算符数量须为标签数 - 1"
  if (scope.tag_operators.some((o) => o !== "and" && o !== "or")) return "运算符只能是 AND 或 OR"
  if (scope.tag_mode === "selected" && scope.tags.length === 0) return "「仅所选标签」模式至少需要一个标签"
  return null
}
