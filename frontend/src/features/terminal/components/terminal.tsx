import { ArrowLeft, Loader2, Plug, Wifi, WifiOff } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { useTerminal } from "../hooks/use-terminal";

interface TerminalViewProps {
  serverId: number;
}

export function TerminalView({ serverId }: TerminalViewProps) {
  const { terminalRef, status, error, connect, disconnect } = useTerminal(serverId);
  const { t } = useTranslation();

  useEffect(() => {
    connect();
    return () => {
      disconnect();
    };
  }, [connect, disconnect]);

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-gray-800">
        <div className="flex items-center gap-4">
          <Link
            to={`/servers/${serverId}`}
            className="inline-flex items-center gap-1.5 text-sm text-gray-500 transition-colors hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
          >
            <ArrowLeft className="h-4 w-4" />
            {t("terminal.backToServer")}
          </Link>
          <span className="h-4 w-px bg-gray-200 dark:bg-gray-800" />
          <span className="flex items-center gap-1.5 text-sm">
            {status === "connected" && (
              <>
                <Wifi className="h-3.5 w-3.5 text-green-400" />
                <span className="text-green-400">{t("terminal.connected")}</span>
              </>
            )}
            {status === "connecting" && (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin text-yellow-400" />
                <span className="text-yellow-400">{t("terminal.connecting")}</span>
              </>
            )}
            {status === "disconnected" && (
              <>
                <WifiOff className="h-3.5 w-3.5 text-gray-400 dark:text-gray-500" />
                <span className="text-gray-400 dark:text-gray-500">{t("terminal.disconnected")}</span>
              </>
            )}
          </span>
        </div>
        <button
          type="button"
          onClick={status === "disconnected" ? connect : disconnect}
          disabled={status === "connecting"}
          className="inline-flex items-center gap-1.5 rounded-lg bg-gray-100 px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-200 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-gray-800 dark:text-gray-300 dark:hover:bg-gray-700"
        >
          <Plug className="h-3.5 w-3.5" />
          {status === "disconnected" ? t("terminal.reconnect") : t("terminal.disconnect")}
        </button>
      </div>

      {error && (
        <div className="border-b border-red-300 bg-red-50 px-4 py-2 dark:border-red-800 dark:bg-red-900/20">
          <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
        </div>
      )}

      <div className="flex-1 overflow-hidden">
        <div ref={terminalRef} className="h-full w-full" />
      </div>
    </div>
  );
}
