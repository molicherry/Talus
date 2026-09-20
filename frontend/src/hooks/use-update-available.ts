import { useEffect, useState } from "react";

import { API_BASE_URL } from "../lib/api-client";
import { usePageVisibility } from "./use-page-visibility";

/** Version baked into this bundle at build time (the release tag, or "dev"). */
const BUILD_VERSION: string | undefined = import.meta.env.VITE_APP_VERSION;

/** How often to re-check while the tab stays visible. */
const POLL_INTERVAL_MS = 10 * 60 * 1000;

/**
 * Nothing to compare against in dev, or when the tag was not baked in. Skipping
 * the check keeps a local `npm run dev` against a deployed backend from sitting
 * under a permanent "new version" banner.
 *
 * The cost is that a source build (`docker compose`, which defaults to
 * VERSION=dev) never shows the banner either: two such builds are
 * indistinguishable, so dropping the guard would only produce false prompts.
 * Set VERSION to something that changes per release — e.g.
 * `VERSION=$(git rev-parse --short HEAD)` — and both images get the same
 * identifier, which is what this comparison needs.
 */
function canDetect(): boolean {
  return !import.meta.env.DEV && !!BUILD_VERSION && BUILD_VERSION !== "dev";
}

/** `null` when unknown (offline, non-2xx) — the next tick tries again. */
async function fetchDeployedVersion(): Promise<string | null> {
  try {
    const res = await fetch(`${API_BASE_URL}/api/v1/version`, { cache: "no-store" });
    if (!res.ok) return null;
    const json = (await res.json()) as { data?: { version?: string } };
    const version = json?.data?.version;
    return typeof version === "string" ? version : null;
  } catch {
    return null;
  }
}

/**
 * Reports whether a different build has been deployed behind this page.
 *
 * The backend reports the version it was built with, which is the same tag the
 * frontend bundle embeds. When they diverge, this tab is running stale code.
 * Revalidated `index.html` makes that rare, but a long-lived tab that started
 * before a deploy still needs a way to notice without a hard refresh — and if
 * the entry chunk itself is gone, the lazy-chunk retry in ErrorBoundary never
 * gets a chance to run.
 */
export function useUpdateAvailable(): boolean {
  const isVisible = usePageVisibility();
  const [updateAvailable, setUpdateAvailable] = useState(false);

  useEffect(() => {
    if (updateAvailable || !canDetect()) return;

    let cancelled = false;
    const check = async () => {
      const deployed = await fetchDeployedVersion();
      if (!cancelled && deployed !== null && deployed !== BUILD_VERSION) {
        setUpdateAvailable(true);
      }
    };

    // Check immediately: this covers a fresh mount and returning to a tab that
    // was hidden across a deploy (the effect re-runs when visibility flips).
    void check();
    if (!isVisible) {
      return () => {
        cancelled = true;
      };
    }

    const id = window.setInterval(check, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [isVisible, updateAvailable]);

  return updateAvailable;
}
