import { Loader2 } from "lucide-react";
import { Suspense, useEffect } from "react";
import { Outlet } from "react-router-dom";
import { prefetchRoutes } from "../../lib/prefetch-routes";
import { ErrorBoundary } from "../ui/error-boundary";
import { Header } from "./header";
import { Sidebar } from "./sidebar";

export function MainLayout() {
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
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header />
        <main className="flex-1 overflow-auto p-6 lg:p-8">
          <ErrorBoundary>
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center py-12">
                  <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
                </div>
              }
            >
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
