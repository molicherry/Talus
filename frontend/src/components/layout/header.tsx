import { LogOut, Monitor, Moon, Sun, User, Key } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../hooks/use-auth";
import type { Theme } from "../../hooks/use-theme";
import { useTheme } from "../../hooks/use-theme";
import { clearAuthToken, getAuthToken } from "../../lib/auth";
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

export function Header() {
  const { user } = useAuth();
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useTheme();
  const [showDropdown, setShowDropdown] = useState(false);
  const [showPasswordModal, setShowPasswordModal] = useState(false);
  const [currentPw, setCurrentPw] = useState("");
  const [newPw, setNewPw] = useState("");
  const [pwError, setPwError] = useState("");
  const [pwOk, setPwOk] = useState(false);

  const handleLogout = () => {
    clearAuthToken();
    window.location.href = "/login";
  };

  const toggleLanguage = () => {
    const next = i18n.language === "zh-CN" ? "en" : "zh-CN";
    i18n.changeLanguage(next);
  };

  const handleChangePassword = async () => {
    setPwError("");
    try {
      const token = getAuthToken();
      const res = await fetch("/api/v1/auth/password", {
        method: "PUT",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ current_password: currentPw, new_password: newPw }),
      });
      if (!res.ok) {
        if (res.status === 401) setPwError(t("auth.wrongPassword"));
        else setPwError(t("common.error"));
        return;
      }
      setPwOk(true);
      setTimeout(() => {
        setShowPasswordModal(false);
        setPwOk(false);
        setCurrentPw("");
        setNewPw("");
      }, 1500);
    } catch {
      setPwError(t("common.error"));
    }
  };

  const ThemeIcon = themeIcons[theme];

  return (
    <header className="flex h-16 items-center justify-between border-b border-border bg-card px-6">
      <h1 className="text-lg font-semibold text-foreground">{t("app.name")}</h1>
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
              type="button"
              variant="ghost"
              onClick={() => setShowDropdown(!showDropdown)}
              className="gap-2"
            >
              <User className="h-4 w-4" />
              <span className="max-w-[10rem] truncate">{user.username}</span>
            </Button>
            {showDropdown && (
              <>
                <div className="fixed inset-0 z-10" onClick={() => setShowDropdown(false)} />
                <div className="absolute right-0 z-20 mt-2 w-52 rounded-xl border border-border bg-card-elevated p-1 shadow-dropdown">
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
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <div className="w-full max-w-sm rounded-xl border border-border bg-card p-6 shadow-elevated">
            <h2 className="text-lg font-semibold text-foreground">{t("auth.changePassword")}</h2>
            {pwOk ? (
              <p className="mt-4 text-sm text-success">{t("auth.passwordChanged")}</p>
            ) : (
              <div className="mt-4 space-y-3">
                <input
                  type="password"
                  value={currentPw}
                  onChange={(e) => setCurrentPw(e.target.value)}
                  className="w-full rounded-lg border border-input bg-card px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                  placeholder={t("auth.currentPassword")}
                />
                <input
                  type="password"
                  value={newPw}
                  onChange={(e) => setNewPw(e.target.value)}
                  className="w-full rounded-lg border border-input bg-card px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                  placeholder={t("auth.newPassword")}
                />
                {pwError && <p className="text-xs text-danger">{pwError}</p>}
                <div className="flex gap-3 pt-1">
                  <Button type="button" onClick={handleChangePassword} className="flex-1">
                    {t("common.save")}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => setShowPasswordModal(false)}
                    className="flex-1"
                  >
                    {t("common.cancel")}
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </header>
  );
}
