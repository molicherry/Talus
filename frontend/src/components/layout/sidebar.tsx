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
    <aside className="flex w-56 flex-col border-r border-gray-200 bg-gray-50 dark:border-gray-800 dark:bg-gray-900">
      <div className="flex h-16 items-center border-b border-gray-200 px-6 dark:border-gray-800">
        <Server className="h-5 w-5 text-blue-600 dark:text-blue-400" />
        <span className="ml-2 text-sm font-semibold text-gray-800 dark:text-gray-200">
          {t("app.name")}
        </span>
      </div>
      <nav className="flex-1 space-y-1 p-4">
        {navItems.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            className={({ isActive }) =>
              `flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-colors ${
                isActive
                  ? "bg-white text-gray-900 dark:bg-gray-800 dark:text-gray-100"
                  : "text-gray-500 hover:bg-gray-100/50 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-gray-800/50 dark:hover:text-gray-200"
              }`
            }
          >
            <Icon className="h-4 w-4" />
            {t(label)}
          </NavLink>
        ))}
      </nav>
      <div className="border-t border-gray-200 px-4 py-3 dark:border-gray-800">
        <p className="text-xs text-gray-400 dark:text-gray-500">{VERSION}</p>
      </div>
    </aside>
  );
}
