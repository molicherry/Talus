import { useSyncExternalStore } from "react";
import en from "./locales/en.json";
import zhCN from "./locales/zh-CN.json";

/**
 * Zero-dependency i18n replacing i18next + react-i18next.
 * The app only uses dot-path lookup, {{var}} interpolation, language
 * detection/persistence, and a language toggle — all covered here.
 * (react-i18next also imported the I18nextProvider; the hook uses
 * useSyncExternalStore instead of context, so no provider is needed.)
 */

type Dict = Record<string, unknown>;

const resources: Record<string, Dict> = {
  en: en as Dict,
  "zh-CN": zhCN as Dict,
};

const STORAGE_KEY = "i18nextLng"; // keep the same key the old detector used

function detectLanguage(): string {
  try {
    const cached = localStorage.getItem(STORAGE_KEY);
    if (cached === "en" || cached === "zh-CN") return cached;
  } catch {
    // storage unavailable — fall through to navigator
  }
  const nav = typeof navigator !== "undefined" ? navigator.language : "";
  return nav.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

function lookup(key: string, dict: Dict): string | undefined {
  let val: unknown = dict;
  for (const part of key.split(".")) {
    if (val === null || typeof val !== "object") return undefined;
    val = (val as Dict)[part];
  }
  return typeof val === "string" ? val : undefined;
}

function translate(
  key: string,
  params?: Record<string, string | number> | string,
): string {
  const defaultValue = typeof params === "string" ? params : undefined;
  let val = lookup(key, currentDict) ?? lookup(key, resources.en);
  if (val === undefined) return defaultValue ?? key; // missing key → default value or key
  if (params && typeof params !== "string") {
    val = val.replace(/\{\{(\w+)\}\}/g, (m, name: string) =>
      name in params ? String(params[name]) : m,
    );
  }
  return val;
}

let language = detectLanguage();
let currentDict = resources[language] ?? resources.en;
const listeners = new Set<() => void>();

function changeLanguage(lang: string): void {
  if (!resources[lang]) lang = "en";
  if (lang === language) return;
  language = lang;
  currentDict = resources[lang];
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    // ignore storage failures
  }
  listeners.forEach((l) => l());
}

export const i18n = {
  get language(): string {
    return language;
  },
  t: translate,
  changeLanguage,
  subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  getSnapshot(): string {
    return language;
  },
};

/** Stable `t` — safe to use in hooks/effects with empty dependency arrays. */
export function useTranslation(): {
  t: (key: string, params?: Record<string, string | number> | string) => string;
  i18n: typeof i18n;
  lang: string;
} {
  useSyncExternalStore(i18n.subscribe, i18n.getSnapshot);
  return { t: translate, i18n, lang: language };
}

export default i18n;
