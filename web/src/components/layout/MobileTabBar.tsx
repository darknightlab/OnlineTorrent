import { NavLink } from "react-router-dom"
import { Home, User } from "lucide-react"
import { useUser } from "@/lib/queries"
import { cn } from "@/lib/utils"

export function MobileTabBar() {
  const { user } = useUser()
  const tabs = [
    { to: "/", label: "首页", icon: Home, end: true },
    { to: user ? "/me" : "/login", label: "我的", icon: User },
  ]
  return (
    <nav
      aria-label="底部导航"
      className="fixed inset-x-0 bottom-0 z-40 border-t bg-header pb-safe backdrop-blur-md md:hidden"
    >
      <div className="grid h-[3.25rem] grid-cols-2">
        {tabs.map((t) => (
          <NavLink
            key={t.label}
            to={t.to}
            end={t.end}
            className={({ isActive }) =>
              cn(
                "flex flex-col items-center justify-center gap-px text-[10.5px] font-medium transition-colors",
                isActive ? "text-primary-text" : "text-muted-foreground",
              )
            }
          >
            {({ isActive }) => (
              <>
                <span className={cn("inline-flex h-6 w-11 items-center justify-center rounded-full transition-colors", isActive && "bg-primary-soft")}>
                  <t.icon className="h-[19px] w-[19px]" strokeWidth={isActive ? 2.4 : 2} />
                </span>
                {t.label}
              </>
            )}
          </NavLink>
        ))}
      </div>
    </nav>
  )
}
