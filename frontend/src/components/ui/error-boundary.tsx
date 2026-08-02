import { AlertTriangle, RefreshCw } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
	  hasError: boolean;
	  isChunkError: boolean;
}

/**
 * Catches render errors (including lazy chunk load failures) and shows a
 * recoverable error screen instead of a white page.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
	state: ErrorBoundaryState = { hasError: false, isChunkError: false };

	static getDerivedStateFromError(): ErrorBoundaryState {
		return { hasError: true, isChunkError: false };
	}

	componentDidCatch(error: Error, info: ErrorInfo) {
		console.error("[ErrorBoundary]", error, info);
		// React.lazy caches a rejected dynamic-import promise: resetting state
		// re-throws the same rejection, so the only reliable recovery for a
		// failed chunk load is a full page reload.
		const isChunkError = /imported module|ChunkLoadError|loading chunk|dynamically imported/i.test(error?.message ?? "");
		if (isChunkError) {
			this.setState({ isChunkError: true });
		}
	}

	private reset = () => {
		if (this.state.isChunkError) {
			window.location.reload();
			return;
		}
		this.setState({ hasError: false });
	};

	render() {
		if (this.state.hasError) {
			return <ErrorFallback onRetry={this.reset} />;
		}
		return this.props.children;
	}
}

function ErrorFallback({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 p-8 text-center">
      <AlertTriangle className="h-10 w-10 text-red-500" />
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">
          {t("common.unexpectedError")}
        </h2>
        <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {t("common.loadError")}
        </p>
      </div>
      <button
        type="button"
        onClick={onRetry}
        className="inline-flex items-center gap-2 rounded-lg bg-gray-100 px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-300 dark:hover:bg-gray-700"
      >
        <RefreshCw className="h-4 w-4" />
        {t("common.retry")}
      </button>
    </div>
  );
}
