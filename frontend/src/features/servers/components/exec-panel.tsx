import { Loader2, Play, RotateCw, Terminal } from "lucide-react";
import { type FormEvent, useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useExecCommand } from "../hooks/use-exec";

interface ExecPanelProps {
  serverId: number;
}

interface HistoryEntry {
  command: string;
  timestamp: number;
}

const MAX_HISTORY = 10;

export function ExecPanel({ serverId }: ExecPanelProps) {
  const execMutation = useExecCommand(serverId);
  const { t } = useTranslation();
  const [command, setCommand] = useState("");
  const [outputTab, setOutputTab] = useState<"stdout" | "stderr">("stdout");
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  const handleSubmit = useCallback(
    (e: FormEvent) => {
      e.preventDefault();
      const trimmed = command.trim();
      if (!trimmed || execMutation.isPending) return;

      execMutation.mutate(
        { command: trimmed },
        {
          onSuccess: () => {
            setHistory((prev) => {
              const next = [{ command: trimmed, timestamp: Date.now() }, ...prev];
              return next.slice(0, MAX_HISTORY);
            });
            setCommand("");
          },
        },
      );
    },
    [command, execMutation],
  );

  const handleHistoryClick = useCallback((cmd: string) => {
    setCommand(cmd);
    inputRef.current?.focus();
  }, []);

  const result = execMutation.data;

  return (
    <div className="space-y-4">
      <form onSubmit={handleSubmit} className="flex gap-3">
        <div className="relative flex-1">
          <Terminal className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-400 dark:text-gray-500" />
          <input
            ref={inputRef}
            type="text"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder={t("exec.placeholder")}
            className="w-full rounded-lg border border-gray-200 bg-white py-2.5 pl-10 pr-3 font-mono text-sm text-gray-800 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200 dark:placeholder:text-gray-500"
            disabled={execMutation.isPending}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <button
          type="submit"
          disabled={!command.trim() || execMutation.isPending}
          className="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {execMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Play className="h-4 w-4" />
          )}
          {t("exec.execute")}
        </button>
      </form>

      {history.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {history.map((entry) => (
            <button
              key={`${entry.timestamp}-${entry.command}`}
              type="button"
              onClick={() => handleHistoryClick(entry.command)}
              className="inline-flex items-center gap-1.5 rounded-md border border-gray-200 bg-gray-100/50 px-2.5 py-1 font-mono text-xs text-gray-700 transition-colors hover:border-gray-400 hover:bg-gray-100 dark:border-gray-700 dark:bg-gray-800/50 dark:text-gray-300 dark:hover:border-gray-600 dark:hover:bg-gray-800"
            >
              <RotateCw className="h-3 w-3 text-gray-400 dark:text-gray-500" />
              {entry.command}
            </button>
          ))}
        </div>
      )}

      {execMutation.isError && !result && (
        <div className="rounded-lg border border-red-300 bg-red-50 p-4 dark:border-red-800 dark:bg-red-900/20">
          <p className="text-sm text-red-600 dark:text-red-400">
            {execMutation.error instanceof Error
              ? execMutation.error.message
              : t("exec.failed")}
          </p>
        </div>
      )}

      {result && (
        <div className="overflow-hidden rounded-lg border border-gray-200 dark:border-gray-800">
          <div className="flex items-center justify-between border-b border-gray-200 bg-gray-50 px-4 py-2 dark:border-gray-800 dark:bg-gray-900">
            <div className="flex gap-1">
              <button
                type="button"
                onClick={() => setOutputTab("stdout")}
                className={`rounded-md px-3 py-1 text-xs font-medium transition-colors ${
                  outputTab === "stdout"
                    ? "bg-gray-200 text-gray-900 dark:bg-gray-700 dark:text-gray-100"
                    : "text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
                }`}
              >
                {t("exec.stdout")}
              </button>
              <button
                type="button"
                onClick={() => setOutputTab("stderr")}
                className={`rounded-md px-3 py-1 text-xs font-medium transition-colors ${
                  outputTab === "stderr"
                    ? "bg-gray-200 text-gray-900 dark:bg-gray-700 dark:text-gray-100"
                    : "text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
                }`}
              >
                {t("exec.stderr")}
              </button>
            </div>
            <div className="flex items-center gap-3 text-xs text-gray-400 dark:text-gray-500">
              <span>
                {t("exec.exitCode")}{" "}
                <span className={result.exit_code === 0 ? "text-green-400" : "text-red-400"}>
                  {result.exit_code}
                </span>
              </span>
              <span>
                {t("exec.duration")} <span className="text-gray-700 dark:text-gray-300">{result.duration_ms}ms</span>
              </span>
            </div>
          </div>
          <pre
            className={`max-h-96 overflow-auto p-4 font-mono text-sm ${
              outputTab === "stdout" ? "text-green-400" : "text-red-400"
            } bg-white whitespace-pre-wrap break-all dark:bg-gray-950`}
          >
            {outputTab === "stdout"
              ? result.stdout || t("exec.noOutput")
              : result.stderr || t("exec.noOutput")}
          </pre>
        </div>
      )}
    </div>
  );
}
