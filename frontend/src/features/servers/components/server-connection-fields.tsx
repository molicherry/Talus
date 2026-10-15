import { Field, Input, Select } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import type { ServerSummary } from "../../../types/models";
import { canKeepProxyPassword, type ConnectionConfig, type ConnectionDraft, type ConnectionField } from "../lib/connection-config";

interface Props {
  draft: ConnectionDraft;
  onChange: (draft: ConnectionDraft) => void;
  saved?: ConnectionConfig;
  candidates?: ServerSummary[];
  serverID?: number;
  errors: Partial<Record<ConnectionField, string>>;
  disabled?: boolean;
  candidatesError?: string;
}
export function ServerConnectionFields({ draft, onChange, saved, candidates, serverID, errors, disabled, candidatesError }: Props) {
  const { t } = useTranslation();
  const change = <K extends keyof ConnectionDraft>(field: K, value: ConnectionDraft[K]) => onChange({ ...draft, [field]: value });
  const error = (field: ConnectionField) => errors[field] ? t(`server.connection.validation.${errors[field]}`) : undefined;
  const choices = candidates?.filter((candidate) => candidate.id !== serverID) ?? [];
  const keepPassword = canKeepProxyPassword(draft, saved);
  return (
    <fieldset className="space-y-4 rounded-xl border border-border p-4" disabled={disabled}>
      <legend className="px-1 text-sm font-medium text-foreground">{t("server.connection.title")}</legend>
      <Field label={t("server.connection.mode")} htmlFor="connection-mode" hint={t("server.connection.targetHint")}>
        <Select id="connection-mode" value={draft.mode} onChange={(event) => change("mode", event.target.value as ConnectionDraft["mode"])}>
          <option value="direct">{t("server.connection.direct")}</option>
          <option value="ssh_jump">{t("server.connection.ssh_jump")}</option>
          <option value="socks5">{t("server.connection.socks5")}</option>
        </Select>
      </Field>
      {draft.mode === "ssh_jump" && (
        <Field label={t("server.connection.jump")} htmlFor="connection-jump" error={error("jumpServerID")} hint={candidatesError ?? t("server.connection.jumpHint")}>
          <Select id="connection-jump" value={draft.jumpServerID} onChange={(event) => change("jumpServerID", event.target.value)}>
            <option value="">{t("server.connection.chooseJump")}</option>
            {draft.jumpServerID && !choices.some((candidate) => String(candidate.id) === draft.jumpServerID) && (
              <option value={draft.jumpServerID} disabled>{t("server.connection.unavailableJump", { id: draft.jumpServerID })}</option>
            )}
            {choices.map((candidate) => {
              const reason = candidate.connection.mode !== "direct" ? t("server.connection.jumpNotDirect") :
                !candidate.credential_id ? t("server.connection.jumpNoCredential") : candidate.host_key_mismatch ? t("server.connection.jumpHostKey") : "";
              return <option key={candidate.id} value={candidate.id} disabled={!!reason}>
                {candidate.name} ({candidate.host}{candidate.port ? `:${candidate.port}` : ""}){reason ? ` — ${reason}` : ""}
              </option>;
            })}
          </Select>
        </Field>
      )}
      {draft.mode === "socks5" && <>
        <div className="grid gap-4 sm:grid-cols-[1fr_7rem]">
          <Field label={t("server.connection.proxyHost")} htmlFor="connection-host" error={error("host")}>
            <Input id="connection-host" value={draft.host} placeholder="proxy.example.com" onChange={(event) => change("host", event.target.value)} />
          </Field>
          <Field label={t("server.connection.proxyPort")} htmlFor="connection-port" error={error("port")}>
            <Input id="connection-port" type="number" min={1} max={65535} value={draft.port} onChange={(event) => change("port", event.target.value)} />
          </Field>
        </div>
        <Field label={t("server.connection.auth")} htmlFor="connection-auth">
          <Select id="connection-auth" value={draft.authType} onChange={(event) => change("authType", event.target.value as ConnectionDraft["authType"])}>
            <option value="none">{t("server.connection.authNone")}</option>
            <option value="username_password">{t("server.connection.authPassword")}</option>
          </Select>
        </Field>
        {draft.authType === "username_password" && <>
          <Field label={t("server.connection.username")} htmlFor="connection-username" error={error("username")}>
            <Input id="connection-username" autoComplete="off" value={draft.username} onChange={(event) => change("username", event.target.value)} />
          </Field>
          <Field label={t("server.connection.password")} htmlFor="connection-password" error={error("password")} hint={t(keepPassword ? "server.connection.passwordKept" : "server.connection.passwordNew")}>
            <Input id="connection-password" type="password" autoComplete="new-password" value={draft.password} placeholder={keepPassword ? t("server.connection.passwordPlaceholder") : undefined} onChange={(event) => change("password", event.target.value)} />
          </Field>
        </>}
        <p className="text-xs text-muted-foreground">{t("server.connection.socksHint")}</p>
      </>}
    </fieldset>
  );
}
