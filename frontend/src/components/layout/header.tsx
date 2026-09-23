import { Key, LogOut, Menu, Monitor, Moon, Sun, User } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "../../i18n";
import { useAuth } from "../../hooks/use-auth";
import type { Theme } from "../../hooks/use-theme";
import { useTheme } from "../../hooks/use-theme";
import { ChangePasswordDialog } from "../../features/auth/components/change-password-dialog";
import { clearAuthToken } from "../../lib/auth";
import { Button } from "../ui/button";

const themeIcons: Record<Theme, typeof Sun> = {
  light: Sun,
  dark: Moon,
  system: Monitor,
};

const themeNext: Record<Theme, Theme> = {
  light: "dark",
  dark: "system",
  system: "light",
};

interface HeaderProps {
  onOpenSidebar: () => void;
  sidebarOpen: boolean;
}

export function Header({ onOpenSidebar, sidebarOpen }: HeaderProps) {
  const { user } = useAuth();
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useTheme();
  const [showDropdown, setShowDropdown] = useState(false);
  const [showPasswordModal, setShowPasswordModal] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!showDropdown) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setShowDropdown(false);
        menuButton.current?.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [showDropdown]);

  const handleLogout = () => {
    clearAuthToken();
    window.location.href = "/login";
  };

  const toggleLanguage = () => {
    const next = i18n.language === "zh-CN" ? "en" : "zh-CN";
    i18n.changeLanguage(next);
  };

  const ThemeIcon = themeIcons[theme];

  return (
    <header className="flex h-16 items-center justify-between gap-3 border-b border-border bg-card px-4 sm:px-6">
      <div className="flex min-w-0 items-center gap-2">
        <button
          type="button"
          onClick={onOpenSidebar}
          aria-label={t("header.openMenu")}
          aria-expanded={sidebarOpen}
          aria-controls="mobile-navigation"
          className="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground lg:hidden"
        >
          <Menu className="h-5 w-5" />
        </button>
        <h1 className="truncate text-lg font-semibold text-foreground">{t("app.name")}</h1>
      </div>
      <div className="flex items-center gap-3">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={() => setTheme(themeNext[theme])}
          title={t(`theme.${theme}`)}
          aria-label={t(`theme.${theme}`)}
        >
          <ThemeIcon className="h-4 w-4" />
        </Button>

        <Button type="button" variant="ghost" size="sm" onClick={toggleLanguage}>
          {t("language.switch")}
        </Button>

        {user && (
          <div className="relative">
            <Button
              ref={menuButton}
              aria-expanded={showDropdown}
              aria-controls="user-actions"
              type="button"
              variant="ghost"
              onClick={() => setShowDropdown(!showDropdown)}
              aria-label={user.username}
              className="gap-2"
            >
              <User className="h-4 w-4" />
              <span className="hidden max-w-[10rem] truncate sm:inline">{user.username}</span>
            </Button>
            {showDropdown && (
              <>
                <div className="fixed inset-0 z-10" onClick={() => setShowDropdown(false)} />
                <div
                  id="user-actions"
                  className="absolute right-0 z-20 mt-2 w-52 rounded-xl border border-border bg-card-elevated p-1 shadow-dropdown"
                >
                  <button
                    type="button"
                    onClick={() => {
                      setShowDropdown(false);
                      setShowPasswordModal(true);
                    }}
                    className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-foreground transition-colors hover:bg-secondary"
                  >
                    <Key className="h-4 w-4 text-muted-foreground" />
                    {t("auth.changePassword")}
                  </button>
                  <button
                    type="button"
                    onClick={handleLogout}
                    className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-danger transition-colors hover:bg-danger-subtle"
                  >
                    <LogOut className="h-4 w-4" />
                    {t("header.logout")}
                  </button>
                </div>
              </>
            )}
          </div>
        )}
      </div>

      {showPasswordModal && (
        <ChangePasswordDialog
          onClose={() => setShowPasswordModal(false)}
          returnFocusRef={menuButton}
        />
      )}
    </header>
  );
}
