import { Fingerprint, Key, LayoutDashboard, Link2, Server, X } from "lucide-react";
import { NavLink } from "react-router-dom";

import { useTranslation } from "../../i18n";
import { cn } from "../../lib/utils";

const VERSION = import.meta.env.VITE_APP_VERSION || "dev";

const navItems = [
  { to: "/", label: "nav.dashboard", icon: LayoutDashboard },
  { to: "/servers", label: "nav.servers", icon: Server },
  { to: "/services", label: "nav.services", icon: Link2 },
  { to: "/credentials", label: "nav.credentials", icon: Key },
  { to: "/api-keys", label: "nav.apiKeys", icon: Fingerprint },
];

interface SidebarProps {
  open: boolean;
  onClose: () => void;
}

export function Sidebar({ open, onClose }: SidebarProps) {
  const { t } = useTranslation();

  return (
    <>
      {/* Mobile backdrop — tapping it closes the drawer. */}
      {open && (
        <button
          type="button"
          aria-label={t("common.closeDialog")}
          onClick={onClose}
          className="fixed inset-0 z-40 bg-black/50 lg:hidden"
        />
      )}

      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex w-60 flex-col border-r border-sidebar-border bg-sidebar transition-transform duration-200 lg:static lg:z-auto lg:translate-x-0",
          open ? "translate-x-0" : "-translate-x-full",
        )}
      >
        <div className="flex h-16 items-center justify-between border-b border-sidebar-border px-6">
          <div className="flex items-center">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm">
              <Server className="h-4 w-4" />
            </div>
            <span className="ml-3 text-sm font-semibold text-sidebar-foreground">
              {t("app.name")}
            </span>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("common.closeDialog")}
            className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground lg:hidden"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <nav className="flex-1 space-y-1 overflow-y-auto p-4">
          {navItems.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              onClick={onClose}
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
    </>
  );
}
