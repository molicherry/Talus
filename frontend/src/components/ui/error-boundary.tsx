import { AlertTriangle, RefreshCw } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode, useEffect } from "react";
import { useLocation } from "react-router-dom";

import { useTranslation } from "../../i18n";
import { isRecentChunkError } from "../../lib/chunk-error";

interface ErrorBoundaryProps {
  children: ReactNode;
  /**
   * When this value changes (e.g. the route pathname), a *render* error state
   * is cleared so navigating away recovers. Chunk errors are kept, because the
   * rejected module import is cached and only a reload can fix it.
   */
  resetKey?: unknown;
  /** `root` renders a full-screen fallback outside the app shell. */
  variant?: "page" | "root";
}

interface ErrorBoundaryState {
  hasError: boolean;
  isChunkError: boolean;
  /** Last seen `resetKey`; a change clears a recoverable render error. */
  seenResetKey: unknown;
}

/**
 * Catches render errors (including lazy chunk load failures) and shows a
 * recoverable error screen instead of a white page.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { hasError: false, isChunkError: false, seenResetKey: undefined };

  static getDerivedStateFromError(): Partial<ErrorBoundaryState> {
    return { hasError: true, isChunkError: false };
  }

  /**
   * Reset a recoverable render error when the caller's key changes. Doing it
   * here (rather than setState in componentDidUpdate) avoids a second render
   * pass and the associated layout thrash.
   */
  static getDerivedStateFromProps(
    props: ErrorBoundaryProps,
    state: ErrorBoundaryState,
  ): Partial<ErrorBoundaryState> | null {
    if (Object.is(props.resetKey, state.seenResetKey)) return null;
    return {
      seenResetKey: props.resetKey,
      ...(state.hasError && !state.isChunkError ? { hasError: false, isChunkError: false } : {}),
    };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("[ErrorBoundary]", error, info);
    // Prefer the explicit signal from main.tsx (vite:preloadError). The message
    // regex stays as a fallback for other bundlers/browsers, not the primary
    // classifier: a network failure must not be misread as "new version".
    const isChunkError =
      isRecentChunkError() ||
      /imported module|ChunkLoadError|loading chunk|dynamically imported/i.test(error?.message ?? "");
    if (isChunkError) {
      this.setState({ isChunkError: true });
    }
  }

  private reset = () => {
    if (this.state.isChunkError) {
      // React.lazy caches the rejected import, so retrying in place re-throws.
      window.location.reload();
      return;
    }
    this.setState({ hasError: false });
  };

  render() {
    if (this.state.hasError) {
      return <ErrorFallback onRetry={this.reset} variant={this.props.variant ?? "page"} />;
    }
    return this.props.children;
  }
}

/** Page boundary that clears a render error when the route changes. */
export function RouteErrorBoundary({ children }: { children: ReactNode }) {
  const location = useLocation();
  return <ErrorBoundary resetKey={location.pathname}>{children}</ErrorBoundary>;
}

function ErrorFallback({ onRetry, variant }: { onRetry: () => void; variant: "page" | "root" }) {
  const { t } = useTranslation();

  // The root boundary may render before React ever committed, in which case the
  // index.html boot overlay is still covering the screen — drop it.
  useEffect(() => {
    if (variant === "root") document.getElementById("boot")?.remove();
  }, [variant]);

  return (
    <div
      className={`flex ${
        variant === "root" ? "min-h-screen" : "h-full"
      } flex-col items-center justify-center gap-4 p-8 text-center`}
    >
      <AlertTriangle className="h-10 w-10 text-danger" />
      <div>
        <h2 className="text-lg font-semibold text-foreground">{t("common.unexpectedError")}</h2>
        <p className="mt-1 text-sm text-muted-foreground">{t("common.loadError")}</p>
      </div>
      <button
        type="button"
        onClick={onRetry}
        className="inline-flex items-center gap-2 rounded-lg border border-border bg-card px-4 py-2 text-sm font-medium text-foreground transition-colors hover:bg-secondary"
      >
        <RefreshCw className="h-4 w-4" />
        {t("common.retry")}
      </button>
    </div>
  );
}
