import { Ban, Crown, Lock } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import type { Access, Role } from "@/lib/types"

const roleText: Record<Role, string> = { guest: "游客", member: "会员", vip: "大会员", admin: "管理员" }

export function RoleBadge({ role }: { role: Role }) {
  const v = role === "admin" ? "accent-soft" : role === "vip" ? "warning" : role === "member" ? "soft" : "muted"
  return <Badge variant={v}>{roleText[role]}</Badge>
}

export const roleLabel = (r: Role) => roleText[r]

/** 种子访问级别：allow 不显示 */
export function AccessBadge({ access, className }: { access: Access; className?: string }) {
  switch (access.action) {
    case "login":
      return (
        <Badge variant="accent-soft" className={className} title={access.reason || "登录后可观看"}>
          <Lock />
          会员
        </Badge>
      )
    case "vip":
      return (
        <Badge variant="warning" className={className} title={access.reason || "大会员可观看"}>
          <Crown />
          大会员
        </Badge>
      )
    case "block":
      return (
        <Badge variant="destructive" className={className} title={access.reason || "已屏蔽"}>
          <Ban />
          屏蔽
        </Badge>
      )
    default:
      return null
  }
}

const actionText = { allow: "允许", login: "需登录", vip: "需大会员", block: "屏蔽" } as const
export function ActionBadge({ action }: { action: Access["action"] }) {
  const v = action === "allow" ? "soft" : action === "login" ? "accent-soft" : action === "vip" ? "warning" : "destructive"
  return <Badge variant={v}>{actionText[action]}</Badge>
}
export const actionLabel = (a: Access["action"]) => actionText[a]
