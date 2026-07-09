import { LogOut, Monitor, Moon, Sun, User, Key } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../hooks/use-auth";
import type { Theme } from "../../hooks/use-theme";
import { useTheme } from "../../hooks/use-theme";
import { clearAuthToken, getAuthToken } from "../../lib/auth";

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
      setTimeout(() => { setShowPasswordModal(false); setPwOk(false); setCurrentPw(""); setNewPw(""); }, 1500);
    } catch {
      setPwError(t("common.error"));
    }
  };

  const ThemeIcon = themeIcons[theme];

  return (
    <header className="flex h-16 items-center justify-between border-b border-gray-200 bg-white px-6 dark:border-gray-800 dark:bg-gray-900">
      <h1 className="text-lg font-semibold text-gray-900 dark:text-gray-100">{t("app.name")}</h1>
      <div className="flex items-center gap-4">
        <button
          type="button"
          onClick={() => setTheme(themeNext[theme])}
          className="rounded-md p-1.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-gray-800 dark:hover:text-gray-200"
          title={t(`theme.${theme}`)}
        >
          <ThemeIcon className="h-4 w-4" />
        </button>
        <button
          type="button"
          onClick={toggleLanguage}
          className="rounded-md px-2.5 py-1.5 text-xs font-medium text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-200 dark:hover:bg-gray-800"
        >
          {t("language.switch")}
        </button>

        {user && (
          <div className="relative">
            <button
              type="button"
              onClick={() => setShowDropdown(!showDropdown)}
              className="inline-flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-gray-800 dark:hover:text-gray-200"
            >
              <User className="h-4 w-4" />
              {user.username}
            </button>
            {showDropdown && (
              <>
                <div className="fixed inset-0 z-10" onClick={() => setShowDropdown(false)} />
                <div className="absolute right-0 z-20 mt-1 w-48 rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-gray-700 dark:bg-gray-800">
                  <button
                    type="button"
                    onClick={() => { setShowDropdown(false); setShowPasswordModal(true); }}
                    className="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"
                  >
                    <Key className="h-4 w-4" />
                    {t("auth.changePassword")}
                  </button>
                  <button
                    type="button"
                    onClick={handleLogout}
                    className="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"
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
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="w-full max-w-sm rounded-xl bg-white p-6 shadow-xl dark:bg-gray-900">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">{t("auth.changePassword")}</h2>
            {pwOk ? (
              <p className="mt-4 text-sm text-green-600 dark:text-green-400">{t("auth.passwordChanged")}</p>
            ) : (
              <div className="mt-4 space-y-3">
                <input
                  type="password"
                  value={currentPw}
                  onChange={(e) => setCurrentPw(e.target.value)}
                  className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
                  placeholder={t("auth.currentPassword")}
                />
                <input
                  type="password"
                  value={newPw}
                  onChange={(e) => setNewPw(e.target.value)}
                  className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
                  placeholder={t("auth.newPassword")}
                />
                {pwError && <p className="text-xs text-red-500">{pwError}</p>}
                <div className="flex gap-3">
                  <button
                    type="button"
                    onClick={handleChangePassword}
                    className="flex-1 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
                  >
                    {t("common.save")}
                  </button>
                  <button
                    type="button"
                    onClick={() => setShowPasswordModal(false)}
                    className="flex-1 rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200"
                  >
                    {t("common.cancel")}
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </header>
  );
}
