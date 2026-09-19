import { Loader2, Play, RotateCw, Terminal } from "lucide-react";
import { type FormEvent, useCallback, useRef, useState } from "react";

import { Button } from "../../../components/ui/button";
import { Input } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { cn } from "../../../lib/utils";
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
      <form onSubmit={handleSubmit} className="flex flex-col gap-3 sm:flex-row">
        <div className="relative flex-1">
          <Terminal className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={inputRef}
            type="text"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder={t("exec.placeholder")}
            className="py-2.5 pl-10 pr-3 font-mono"
            disabled={execMutation.isPending}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <Button type="submit" disabled={!command.trim() || execMutation.isPending} className="py-2.5">
          {execMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Play className="h-4 w-4" />
          )}
          {t("exec.execute")}
        </Button>
      </form>

      {history.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {history.map((entry) => (
            <button
              key={`${entry.timestamp}-${entry.command}`}
              type="button"
              onClick={() => handleHistoryClick(entry.command)}
              className="inline-flex max-w-full items-center gap-1.5 truncate rounded-md border border-border bg-muted/50 px-2.5 py-1 font-mono text-xs text-foreground transition-colors hover:border-border-hover hover:bg-secondary"
            >
              <RotateCw className="h-3 w-3 shrink-0 text-muted-foreground" />
              <span className="truncate">{entry.command}</span>
            </button>
          ))}
        </div>
      )}

      {execMutation.isError && !result && (
        <div className="rounded-lg border border-danger/30 bg-danger-subtle p-4">
          <p className="text-sm text-danger">
            {execMutation.error instanceof Error ? execMutation.error.message : t("exec.failed")}
          </p>
        </div>
      )}

      {result && (
        <div className="overflow-hidden rounded-2xl border border-border">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-muted/60 px-4 py-2">
            <div className="flex gap-1">
              {(["stdout", "stderr"] as const).map((tab) => (
                <button
                  key={tab}
                  type="button"
                  onClick={() => setOutputTab(tab)}
                  className={cn(
                    "rounded-md px-3 py-1 text-xs font-medium transition-colors",
                    outputTab === tab
                      ? "bg-card text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {tab === "stdout" ? t("exec.stdout") : t("exec.stderr")}
                </button>
              ))}
            </div>
            <div className="flex items-center gap-3 text-xs text-muted-foreground">
              <span>
                {t("exec.exitCode")}{" "}
                <span
                  className={cn(
                    "font-mono tabular-nums",
                    result.exit_code === 0 ? "text-success" : "text-danger",
                  )}
                >
                  {result.exit_code}
                </span>
              </span>
              <span>
                {t("exec.duration")}{" "}
                <span className="font-mono tabular-nums text-foreground">{result.duration_ms}ms</span>
              </span>
            </div>
          </div>
          {/* Inverted console surface: readable in both light and dark themes. */}
          <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all bg-foreground p-4 font-mono text-sm text-background">
            {outputTab === "stdout"
              ? result.stdout || t("exec.noOutput")
              : result.stderr || t("exec.noOutput")}
          </pre>
        </div>
      )}
    </div>
  );
}
