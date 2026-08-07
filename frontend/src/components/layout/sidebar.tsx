import { Fingerprint, Key, LayoutDashboard, Link2, Server } from "lucide-react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router-dom";

const VERSION = import.meta.env.VITE_APP_VERSION || "dev";

const navItems = [
  { to: "/", label: "nav.dashboard", icon: LayoutDashboard },
  { to: "/servers", label: "nav.servers", icon: Server },
  { to: "/services", label: "nav.services", icon: Link2 },
  { to: "/credentials", label: "nav.credentials", icon: Key },
  { to: "/api-keys", label: "nav.apiKeys", icon: Fingerprint },
];

export function Sidebar() {
  const { t } = useTranslation();

  return (
    <aside className="flex w-60 flex-col border-r border-sidebar-border bg-sidebar">
      <div className="flex h-16 items-center border-b border-sidebar-border px-6">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm">
          <Server className="h-4 w-4" />
        </div>
        <span className="ml-3 text-sm font-semibold text-sidebar-foreground">{t("app.name")}</span>
      </div>

      <nav className="flex-1 space-y-1 p-4">
        {navItems.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            className={({ isActive }) =>
              `group relative flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all ${
                isActive
                  ? "bg-sidebar-active text-sidebar-active-foreground"
                  : "text-sidebar-foreground hover:bg-secondary hover:text-foreground"
              }`
            }
          >
            {({ isActive }) => (
              <>
                {isActive && (
                  <span
                    className="absolute -left-4 top-1/2 h-5 w-1 -translate-y-1/2 rounded-r bg-primary"
                    aria-hidden="true"
                  />
                )}
                <Icon className="h-4 w-4 shrink-0 transition-colors" />
                <span className="truncate">{t(label)}</span>
              </>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="border-t border-sidebar-border px-4 py-3">
        <p className="text-xs font-medium text-muted-foreground">{VERSION}</p>
      </div>
    </aside>
  );
}
