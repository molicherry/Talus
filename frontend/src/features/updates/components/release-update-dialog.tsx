import { ExternalLink } from "lucide-react";
import type { RefObject } from "react";

import { Dialog } from "../../../components/ui/dialog";
import { useTranslation } from "../../../i18n";
import type { Release } from "../api";
import { selectReleaseNotes } from "../lib/release-notes";
import { ReleaseMarkdown } from "./release-markdown";

interface Props {
  open: boolean;
  onClose: () => void;
  currentVersion: string;
  release: Release;
  returnFocusRef: RefObject<HTMLButtonElement | null>;
}

export function ReleaseUpdateDialog({ open, onClose, currentVersion, release, returnFocusRef }: Props) {
  const { t, lang } = useTranslation();
  const notes = selectReleaseNotes(release.notes, lang);
  return (
    <Dialog
      id="release-update-dialog"
      open={open}
      onClose={onClose}
      title={t("releaseUpdate.title", { version: release.version })}
      returnFocusRef={returnFocusRef}
    >
      <dl className="mb-5 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t("releaseUpdate.currentVersion")}</dt>
        <dd className="break-all font-medium">{currentVersion}</dd>
        <dt className="text-muted-foreground">{t("releaseUpdate.latestVersion")}</dt>
        <dd className="break-all font-medium">{release.version}</dd>
        <dt className="text-muted-foreground">{t("releaseUpdate.publishedAt")}</dt>
        <dd><time dateTime={release.published_at}>{new Date(release.published_at).toLocaleDateString(lang)}</time></dd>
      </dl>
      <h3 className="mb-2 text-sm font-semibold">{t("releaseUpdate.releaseNotes")}</h3>
      <div className="max-h-[40vh] overflow-y-auto rounded-xl border border-border bg-background p-4">
        {notes ? <ReleaseMarkdown notes={notes} /> : <p className="text-sm text-muted-foreground">{t("releaseUpdate.noNotes")}</p>}
      </div>
      <div className="mt-5 flex flex-wrap items-center justify-between gap-3">
        <a
          href="https://github.com/molicherry/Talus/releases"
          target="_blank"
          rel="noopener noreferrer"
          className="touch-target inline-flex items-center text-sm text-link underline hover:text-link-hover"
        >
          {t("releaseUpdate.allReleases")}
        </a>
        <a
          href={release.url}
          target="_blank"
          rel="noopener noreferrer"
          className="touch-target inline-flex items-center gap-2 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover"
        >
          {t("releaseUpdate.openRelease")}
          <ExternalLink className="h-4 w-4" aria-hidden="true" />
        </a>
      </div>
    </Dialog>
  );
}
