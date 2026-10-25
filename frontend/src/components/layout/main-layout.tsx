import { Loader2 } from "lucide-react";
import { Suspense, useEffect, useLayoutEffect, useState } from "react";
import { Outlet } from "react-router-dom";

import { prefetchRoutes } from "../../lib/prefetch-routes";
import { RouteErrorBoundary } from "../ui/error-boundary";
import { Header } from "./header";
import { UpdateBanner } from "./update-banner";
import { Sidebar } from "./sidebar";

export function MainLayout() {
  const [sidebarOpen, setSidebarOpen] = useState(false);

  // The document scrolls, so own its header offset for this layout's lifetime.
  // Apply before paint/anchor effects; cleanup also covers SPA navigation to login.
  useLayoutEffect(() => {
    const root = document.documentElement;
    root.classList.add("talus-app-layout");
    return () => root.classList.remove("talus-app-layout");
  }, []);

  // Warm the lazy route chunks in the background once the app shell mounts
  // (after login), so first visits to other sections are instant.
  useEffect(() => {
    if (typeof window.requestIdleCallback === "function") {
      const id = window.requestIdleCallback(() => prefetchRoutes(), { timeout: 3000 });
      return () => window.cancelIdleCallback(id);
    }
    const id = window.setTimeout(prefetchRoutes, 1000);
    return () => window.clearTimeout(id);
  }, []);

  return (
    <div className="flex min-h-screen bg-background text-foreground">
      <Sidebar open={sidebarOpen} onClose={() => setSidebarOpen(false)} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Header sidebarOpen={sidebarOpen} onOpenSidebar={() => setSidebarOpen(true)} />
        <UpdateBanner />
        <main className="min-w-0 flex-1 p-4 sm:p-6 lg:p-8">
          <RouteErrorBoundary>
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center py-12">
                  <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
                </div>
              }
            >
              <Outlet />
            </Suspense>
          </RouteErrorBoundary>
        </main>
      </div>
    </div>
  );
}
