import { Loader2 } from "lucide-react";
import { Suspense } from "react";
import { Outlet } from "react-router-dom";
import { ErrorBoundary } from "../ui/error-boundary";
import { Header } from "./header";
import { Sidebar } from "./sidebar";

export function MainLayout() {
  return (
    <div className="flex h-screen bg-white dark:bg-gray-950">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header />
        <main className="flex-1 overflow-auto p-6">
          <ErrorBoundary>
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center py-12">
                  <Loader2 className="h-6 w-6 animate-spin text-gray-500 dark:text-gray-400" />
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
