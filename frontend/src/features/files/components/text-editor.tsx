import { Loader2, RotateCcw, Save } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { Dialog } from "../../../components/ui/dialog";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { translateApiError } from "../../../lib/api-error";
import { toast } from "../../../lib/toast";
import * as files from "../api";

export function TextEditor({ serverId, initial, onClose, onSaved }: { serverId: number; initial: files.RemoteText; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const [saved, setSaved] = useState(initial);
  const [content, setContent] = useState(initial.content);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [discard, setDiscard] = useState<"close" | "reload" | null>(null);
  const controller = useRef<AbortController | null>(null);
  const dirty = content !== saved.content;
  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    if (!dirty) return;
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", prevent);
    return () => window.removeEventListener("beforeunload", prevent);
  }, [dirty]);
  const perform = async (kind: "save" | "reload") => {
    if (controller.current) return;
    const active = new AbortController(); controller.current = active;
    setPending(true); setError(null);
    try {
      if (kind === "save" && new TextEncoder().encode(content).byteLength > 1024 * 1024) { setError(t("errors.sftp_text_too_large")); return; }
      const result = kind === "save" ? await files.saveText(serverId, { ...saved, content }, active.signal) : await files.readText(serverId, saved.path, active.signal);
      if (active.signal.aborted) return;
      setSaved(result); setContent(result.content); setConflict(false);
      if (kind === "save") { toast.success(t("files.saved")); onSaved(); }
    } catch (error) {
      if (active.signal.aborted) return;
      const changed = error instanceof ApiClientError && error.reason === "sftp_conflict";
      setConflict(changed);
      setError(changed ? t("files.conflict") : translateApiError(error, t, t("files.error")));
    } finally {
      if (controller.current === active) controller.current = null;
      if (!active.signal.aborted) setPending(false);
    }
  };
  const request = (action: "close" | "reload") => {
    if (pending) return;
    if (dirty) setDiscard(action);
    else if (action === "close") onClose();
    else void perform("reload");
  };
  return <>
    <Dialog open title={t("files.editorTitle", { name: saved.path.split("/").at(-1) ?? "" })} onClose={() => request("close")} dismissible={!pending && !discard} className="max-w-4xl">
      <p className="mb-3 text-xs text-muted-foreground">{t("files.editorHint")}</p>
      <p className="mb-3 break-all font-mono text-xs text-muted-foreground">{saved.path}</p>
      {error && <p role="alert" className="mb-3 text-sm text-danger">{error}</p>}
      <textarea aria-label={t("files.edit")} value={content} onChange={event => setContent(event.target.value)} disabled={pending} spellCheck={false} className="sftp-editor" />
      <div className="mt-4 flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" disabled={pending} onClick={() => request("reload")}><RotateCcw size={14} />{t("files.reload")}</Button>
        <Button type="button" disabled={pending || !dirty || conflict} onClick={() => void perform("save")}>{pending ? <Loader2 size={14} className="animate-spin" /> : <Save size={14} />}{t("files.save")}</Button>
      </div>
    </Dialog>
    <ConfirmDialog open={!!discard} title={t("files.discardTitle")} message={t("files.discardBody")} confirmLabel={t("files.discard")} onCancel={() => setDiscard(null)} onConfirm={() => { const action = discard; setDiscard(null); if (action === "close") onClose(); else void perform("reload"); }} />
  </>;
}
