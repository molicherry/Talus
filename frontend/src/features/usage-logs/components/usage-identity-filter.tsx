import { ChevronDown, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { Button } from "../../../components/ui/button";
import { Field, Input } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { getAuthSnapshot } from "../../../lib/auth";
import { translateApiError } from "../../../lib/api-error";
import { getUsageFilterOptions } from "../api";
import { validEntityID } from "../lib/session";
import type { UsageIdentityKind, UsageIdentityOption, UsageIdentityOptions } from "../types";

interface Props {
  id: string;
  kind: UsageIdentityKind;
  value?: string;
  onChange: (id?: string) => void;
  authEpoch: number;
}
interface SearchState {
  key: string;
  status: "loading" | "success" | "error";
  data?: UsageIdentityOptions;
  error?: unknown;
}
interface LabelState {
  key: string;
  status: "loading" | "success" | "error";
  item?: UsageIdentityOption;
  error?: unknown;
}
function accessIsCurrent(epoch: number): boolean {
  const auth = getAuthSnapshot();
  return auth.authEpoch === epoch && auth.canViewUsageLogs;
}

/** IDs stay decimal strings through search, selection, manual input and URLs. */
export function UsageIdentityFilter({ id, kind, value, onChange, authEpoch }: Props) {
  const { t } = useTranslation();
  const label = t(`usage.identity.labels.${kind}`);
  const listID = useId();
  const hintID = `${listID}-hint`;
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const searchGeneration = useRef(0);
  const labelGeneration = useRef(0);
  const labelController = useRef<AbortController | null>(null);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [active, setActive] = useState(-1);
  const [searchRetry, setSearchRetry] = useState(0);
  const [labelRetry, setLabelRetry] = useState(0);
  const [results, setResults] = useState<SearchState>();
  const [selected, setSelected] = useState<LabelState>();
  const selectedRef = useRef<LabelState | undefined>(undefined);
  selectedRef.current = selected;
  const needle = search.trim();
  const searchKey = JSON.stringify([authEpoch, kind, needle]);
  const labelKey = JSON.stringify([authEpoch, kind, value]);
  const currentResults = results?.key === searchKey ? results : undefined;
  const selectedState = selected?.key === labelKey ? selected : undefined;
  const items = currentResults?.status === "success" ? currentResults.data?.items ?? [] : [];
  const manualID = validEntityID(needle) ? needle : undefined;
  const invalidManualID = /^[+-]?\d+$/.test(needle) && !manualID;
  const includeManual = !!manualID && !items.some(item => item.id === manualID);
  const choices = [{ id: undefined, item: undefined, manual: false }, ...items.map(item => ({ id: item.id, item, manual: false })), ...(includeManual ? [{ id: manualID, item: undefined, manual: true }] : [])];
  const optionLabel = (option: UsageIdentityOption) => [option.name || t("usage.unknown"), option.prefix, `#${option.id}`, option.deleted ? t("usage.identity.deleted") : undefined].filter(Boolean).join(" · ");
  const selectedText = value ? selectedState?.item ? optionLabel(selectedState.item) : t("usage.identity.historicalID", { id: value }) : "";

  // URL-restored/manual selections resolve their label without replacing the ID
  // when the resource or retained snapshot no longer exists.
  useEffect(() => {
    if (!value) return;
    if (!validEntityID(value)) { setSelected({ key: labelKey, status: "success" }); return; }
    const existing = selectedRef.current;
    if (existing?.key === labelKey && existing.status === "success") return;
    const controller = new AbortController();
    const generation = ++labelGeneration.current;
    labelController.current = controller;
    setSelected({ key: labelKey, status: "loading" });
    void getUsageFilterOptions(kind, value, controller.signal).then(data => {
      if (controller.signal.aborted || generation !== labelGeneration.current || !accessIsCurrent(authEpoch)) return;
      setSelected({ key: labelKey, status: "success", item: data.items.find(item => item.id === value) });
    }, error => {
      if (controller.signal.aborted || generation !== labelGeneration.current || !accessIsCurrent(authEpoch)) return;
      setSelected({ key: labelKey, status: "error", error });
    });
    return () => controller.abort();
  }, [authEpoch, kind, value, labelKey, labelRetry]);

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const generation = ++searchGeneration.current;
    setResults({ key: searchKey, status: "loading" });
    const timer = window.setTimeout(() => {
      void getUsageFilterOptions(kind, needle, controller.signal).then(data => {
        if (controller.signal.aborted || generation !== searchGeneration.current || !accessIsCurrent(authEpoch)) return;
        setActive(index => index === 0 ? 0 : -1);
        setResults({ key: searchKey, status: "success", data });
      }, error => {
        if (controller.signal.aborted || generation !== searchGeneration.current || !accessIsCurrent(authEpoch)) return;
        setResults({ key: searchKey, status: "error", error });
      });
    }, 250);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [open, authEpoch, kind, needle, searchKey, searchRetry]);

  useEffect(() => {
    if (open && active >= 0) document.getElementById(`${listID}-${active}`)?.scrollIntoView({ block: "nearest" });
  }, [open, active, listID]);

  const choose = (choice: typeof choices[number]) => {
    if (choice.item) {
      ++labelGeneration.current; labelController.current?.abort();
      setSelected({ key: JSON.stringify([authEpoch, kind, choice.id]), status: "success", item: choice.item });
    }
    onChange(choice.id);
    setOpen(false); setSearch(""); setActive(-1);
  };
  const clear = () => { ++labelGeneration.current; labelController.current?.abort(); onChange(undefined); setOpen(false); setSearch(""); setActive(-1); };
  const loading = !currentResults || currentResults.status === "loading";
  const openList = () => { if (!open) { setSearch(""); setOpen(true); setActive(-1); } };

  return <Field label={label} htmlFor={id}>
    <div ref={root} className="relative" onBlur={event => {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
    }}>
      <div className="flex flex-wrap items-center gap-2">
        <Input ref={input} className="min-w-0 flex-1" id={id} role="combobox" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded={open}
          aria-controls={open ? listID : undefined} aria-activedescendant={open && active >= 0 && choices[active] ? `${listID}-${active}` : undefined}
          aria-describedby={hintID} autoComplete="off" spellCheck={false} maxLength={128}
          placeholder={t("usage.identity.search")} title={selectedText || undefined} value={open ? search : selectedText}
          onFocus={openList} onClick={openList}
          onCompositionStart={openList}
          onPaste={event => { if (!open) { event.preventDefault(); setSearch(event.clipboardData.getData("text").slice(0, 128)); setOpen(true); setActive(-1); } }}
          onChange={event => { setSearch(event.target.value); setOpen(true); setActive(-1); }}
          onKeyDown={event => {
            if (!open && !event.ctrlKey && !event.metaKey && !event.altKey && (event.key.length === 1 || event.key === "Backspace" || event.key === "Delete")) {
              event.preventDefault(); setSearch(event.key.length === 1 ? event.key : ""); setOpen(true); setActive(-1);
            } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault(); setOpen(true);
              if (!open) { setSearch(""); setActive(0); }
              else setActive(index => event.key === "ArrowDown" ? (index + 1) % choices.length : (index <= 0 ? choices.length - 1 : index - 1));
            } else if (event.key === "Enter" && open) {
              event.preventDefault();
              if (active >= 0 && choices[active]) choose(choices[active]);
              else if (manualID) choose(choices.find(choice => choice.id === manualID)!);
            } else if (event.key === "Escape" && open) {
              event.preventDefault(); event.stopPropagation(); setOpen(false); setActive(-1);
            }
          }}
        />
        <Button type="button" variant="ghost" size="icon" className="shrink-0" aria-label={t("usage.identity.toggle", { label })} aria-expanded={open} aria-controls={open ? listID : undefined} onMouseDown={event => event.preventDefault()} onClick={() => { setOpen(previous => !previous); setSearch(""); setActive(-1); input.current?.focus(); }}><ChevronDown className={`h-4 w-4 transition-transform ${open ? "rotate-180" : ""}`} /></Button>
        {value && <Button type="button" variant="ghost" size="icon" aria-label={t("usage.identity.clear", { label })} onClick={clear}><X className="h-4 w-4" /></Button>}
      </div>
      {value && <p className="mt-1 break-words text-xs text-foreground">{t("usage.identity.selectedID", { id: value })}{selectedState?.item?.deleted ? ` · ${t("usage.identity.deleted")}` : ""}</p>}
      <p id={hintID} className="mt-1 text-xs text-muted-foreground">{t("usage.identity.manualHint")}</p>
      {value && selectedState?.status === "loading" && <p className="mt-1 text-xs text-muted-foreground" role="status">{t("usage.identity.resolving")}</p>}
      {value && !validEntityID(value) && <p className="mt-1 text-xs text-danger" role="alert">{t("usage.identity.invalidID")}</p>}
      {value && validEntityID(value) && selectedState?.status === "success" && !selectedState.item && <p className="mt-1 text-xs text-muted-foreground">{t("usage.identity.missingLabel")}</p>}
      {value && selectedState?.status === "error" && <div className="mt-1 text-xs text-danger" role="alert">{t("usage.identity.labelFailed")} <Button type="button" size="sm" variant="outline" onClick={() => setLabelRetry(retry => retry + 1)}>{t("common.retry")}</Button></div>}
      {open && <div className="absolute top-full z-30 mt-1 w-full min-w-0 rounded-lg border border-border bg-card shadow-dropdown">
        <div id={listID} role="listbox" aria-label={label} aria-busy={loading} className="max-h-64 overflow-y-auto p-1">
          {choices.map((choice, index) => <button type="button" role="option" id={`${listID}-${index}`} key={choice.id ?? "all"} tabIndex={-1}
            aria-selected={choice.id === value} className={`touch-target block w-full rounded-md px-3 py-2 text-left text-sm transition-colors ${active === index ? "bg-primary-subtle text-foreground" : "hover:bg-secondary"}`}
            onMouseDown={event => event.preventDefault()} onClick={() => choose(choice)} onPointerMove={() => setActive(index)}>
            <span className="block break-words">{choice.item ? optionLabel(choice.item) : choice.manual ? t("usage.identity.useID", { id: choice.id! }) : t("usage.all")}</span>
          </button>)}
        </div>
        <div className="border-t border-border px-3 py-2 text-xs">
          {loading && <p className="text-muted-foreground" role="status">{t("common.loading")}</p>}
          {currentResults?.status === "error" && <div className="text-danger" role="alert">{translateApiError(currentResults.error, t)} <Button type="button" variant="outline" size="sm" onMouseDown={event => event.preventDefault()} onClick={() => { input.current?.focus(); setActive(-1); setSearchRetry(retry => retry + 1); }}>{t("common.retry")}</Button></div>}
          {invalidManualID && <p className="text-danger" role="alert">{t("usage.identity.invalidID")}</p>}
          {currentResults?.status === "success" && !invalidManualID && items.length === 0 && <p className="text-muted-foreground" role="status">{t("usage.identity.noResults")}</p>}
          {currentResults?.data?.has_more && <p className="text-muted-foreground" role="status">{t("usage.identity.more")}</p>}
          {currentResults?.status === "success" && items.length > 0 && !currentResults.data?.has_more && <p className="sr-only" role="status">{t("usage.identity.matches", { count: items.length })}</p>}
        </div>
      </div>}
    </div>
  </Field>;
}
