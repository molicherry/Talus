import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { Providers } from "./app/providers";
import { AppRouter } from "./app/router";
import { ErrorBoundary } from "./components/ui/error-boundary";
import { markChunkError } from "./lib/chunk-error";
import "./index.css";

// Vite fires this when a hashed chunk fails to load (e.g. a tab that predates a
// redeploy asks for a deleted file). Record it so the boundary shows the reload
// path without parsing the error message. Deliberately NOT calling
// preventDefault: the import rejection must still reach the boundary, instead
// of being swallowed into a blank page.
window.addEventListener("vite:preloadError", () => {
  markChunkError();
});

const rootElement = document.getElementById("root");
if (!rootElement) throw new Error("Root element not found");
createRoot(rootElement).render(
  <StrictMode>
    {/* Synchronous, and outside Providers so a failure in the providers
        themselves (router/toaster) or a lazy layout still has a recovery UI. */}
    <ErrorBoundary variant="root">
      <Providers>
        <AppRouter />
      </Providers>
    </ErrorBoundary>
  </StrictMode>,
);
