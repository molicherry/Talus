/**
 * Background prefetch of lazy route chunks.
 *
 * Every page in router.tsx is code-split via React.lazy(), so the first visit
 * to a route pays the full download + parse + execute cost of its chunk. After
 * the app shell mounts (i.e. right after login), we warm the module cache by
 * importing all route chunks in the background. Dynamic imports are cached per
 * specifier, so the later React.lazy() call resolves instantly.
 *
 * Order matters: main-nav list pages first (what "switching sections" hits),
 * then the heavy pages (terminal + server detail with monitoring charts).
 */

type RouteLoader = () => Promise<unknown>;

const ROUTE_LOADERS: RouteLoader[] = [
  // The post-login landing page first — DashboardPage became lazy in the
  // initial-load work, so warm it so the first route after login is instant.
  () => import("../features/dashboard/components/dashboard-page"),
  // Main nav list pages — small, cover tab switching.
  () => import("../features/servers/components/server-list-page"),
  () => import("../features/services/components/service-list-page"),
  () => import("../features/credentials/components/credential-list-page"),
  () => import("../features/auth/components/api-keys-page"),
  // Heaviest pages — biggest first-visit win.
  () => import("../features/terminal/components/terminal-page"),
  () => import("../features/servers/components/server-detail-page"),
  // Secondary / form pages.
  () => import("../features/servers/components/exec-page"),
  () => import("../features/servers/components/server-create-page"),
  () => import("../features/servers/components/server-edit-page"),
  () => import("../features/credentials/components/credential-create-page"),
  () => import("../features/credentials/components/credential-edit-page"),
  () => import("../features/services/components/service-create-page"),
  () => import("../features/services/components/service-edit-page"),
];

/** Warm the module cache for every lazy route chunk. Failures are non-fatal. */
export function prefetchRoutes(): void {
  for (const load of ROUTE_LOADERS) {
    void load().catch(() => {
      // A failed prefetch only means the first real visit downloads normally.
    });
  }
}
