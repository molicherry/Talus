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

      <div className="flex items-center justify-between gap-3 border-t border-sidebar-border px-4 py-3">
        <p className="truncate text-xs font-medium text-muted-foreground">{VERSION}</p>
        <a
          href="https://github.com/molicherry/Talus"
          target="_blank"
          rel="noopener noreferrer"
          aria-label={t("app.githubRepository")}
          title={t("app.githubRepository")}
          className="touch-target inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-sidebar"
        >
          <svg viewBox="0 0 16 16" fill="currentColor" className="h-4 w-4" aria-hidden="true">
            <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.65 7.65 0 0 1 4 0c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
          </svg>
        </a>
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
