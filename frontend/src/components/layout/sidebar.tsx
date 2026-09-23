import { Fingerprint, Key, LayoutDashboard, Link2, Server } from "lucide-react";
import { useEffect, useState } from "react";
import { Dialog } from "../ui/dialog";
import { NavLink } from "react-router-dom";

import { useTranslation } from "../../i18n";
import { Logo } from "../ui/logo";

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

  const [desktop, setDesktop] = useState(() => window.matchMedia("(min-width: 1024px)").matches);
  useEffect(() => {
    const media = window.matchMedia("(min-width: 1024px)");
    const update = () => setDesktop(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    if (desktop && open) onClose();
  }, [desktop, open, onClose]);

  const content = (
    <>
      <nav className="flex-1 space-y-1 overflow-y-auto p-4">
        {navItems.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            onClick={onClose}
            className={({ isActive }) =>
              `touch-target group relative flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all ${
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
    </>
  );

  if (!desktop) {
    return (
      <Dialog
        id="mobile-navigation"
        open={open}
        onClose={onClose}
        title={t("app.name")}
        className="m-0 mr-auto h-dvh max-h-none w-60 max-w-[calc(100vw-3rem)] rounded-none border-y-0 border-l-0 bg-sidebar"
        contentClassName="flex h-full flex-col p-4 [&_nav]:p-0"
      >
        {content}
      </Dialog>
    );
  }

  return (
    <aside className="flex w-60 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
      <div className="flex h-16 items-center border-b border-sidebar-border px-6">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-white shadow-sm">
          <Logo className="h-4.5 w-4.5" />
        </div>
        <span className="ml-3 text-sm font-semibold text-sidebar-foreground">{t("app.name")}</span>
      </div>
      {content}
    </aside>
  );
}
