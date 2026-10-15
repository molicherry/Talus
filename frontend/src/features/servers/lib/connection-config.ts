import { z } from "zod";

export const ConnectionModeSchema = z.enum(["direct", "ssh_jump", "socks5"]);
export type ConnectionMode = z.infer<typeof ConnectionModeSchema>;

// Detail and summary responses intentionally share this safe, secret-free shape.
export const ConnectionConfigSchema = z.object({
  mode: ConnectionModeSchema,
  jump_server_id: z.number().int().positive().optional(),
  host: z.string().optional(),
  port: z.number().int().min(1).max(65535).optional(),
  auth_type: z.enum(["none", "username_password"]).optional(),
  username: z.string().optional(),
  has_password: z.boolean().optional(),
});
export type ConnectionConfig = z.infer<typeof ConnectionConfigSchema>;

export const ConnectionInputSchema = z.union([
  z.object({ mode: z.literal("direct") }).strict(),
  z.object({ mode: z.literal("ssh_jump"), jump_server_id: z.number().int().positive() }).strict(),
  z.object({ mode: z.literal("socks5"), host: z.string().min(1), port: z.number().int().min(1).max(65535),
    auth_type: z.literal("none") }).strict(),
  z.object({ mode: z.literal("socks5"), host: z.string().min(1), port: z.number().int().min(1).max(65535),
    auth_type: z.literal("username_password"), username: z.string().min(1), password: z.string().min(1).optional() }).strict(),
]);
export type ConnectionInput = z.infer<typeof ConnectionInputSchema>;

export interface ConnectionDraft {
  mode: ConnectionMode;
  jumpServerID: string;
  host: string;
  port: string;
  authType: "none" | "username_password";
  username: string;
  password: string;
}
export function connectionDraft(config?: ConnectionConfig): ConnectionDraft {
  return { mode: config?.mode ?? "direct", jumpServerID: String(config?.jump_server_id ?? ""),
    host: config?.host ?? "", port: String(config?.port ?? 1080), authType: config?.auth_type ?? "none",
    username: config?.username ?? "", password: "" };
}
const normalizedHost = (host: string) => host.trim().replace(/^\[|\]$/g, "").replace(/\.$/, "").toLowerCase();
export function canKeepProxyPassword(draft: ConnectionDraft, saved?: ConnectionConfig): boolean {
  return saved?.mode === "socks5" && saved.auth_type === "username_password" && saved.has_password === true &&
    draft.mode === "socks5" && draft.authType === "username_password" &&
    normalizedHost(draft.host) === normalizedHost(saved.host ?? "") && Number(draft.port) === saved.port &&
    draft.username === saved.username;
}
export type ConnectionField = "jumpServerID" | "host" | "port" | "username" | "password";
export function connectionErrors(draft: ConnectionDraft, saved?: ConnectionConfig): Partial<Record<ConnectionField, string>> {
  const errors: Partial<Record<ConnectionField, string>> = {};
  if (draft.mode === "ssh_jump" && (!Number.isInteger(Number(draft.jumpServerID)) || Number(draft.jumpServerID) <= 0))
    errors.jumpServerID = "jumpRequired";
  if (draft.mode !== "socks5") return errors;
  const host = normalizedHost(draft.host);
  if (!host || /[\s/@?#\\]/.test(host) || (host.includes(":") && !/^[\da-f:.]+$/i.test(host))) errors.host = "hostInvalid";
  const port = Number(draft.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) errors.port = "portInvalid";
  if (draft.authType === "username_password") {
    const bytes = (value: string) => new TextEncoder().encode(value).length;
    if (bytes(draft.username) < 1 || bytes(draft.username) > 255) errors.username = "usernameLength";
    if ((!draft.password && !canKeepProxyPassword(draft, saved)) || bytes(draft.password) > 255) errors.password = "passwordRequired";
  }
  return errors;
}
export function serializeConnection(draft: ConnectionDraft): ConnectionInput {
  if (draft.mode === "direct") return { mode: "direct" };
  if (draft.mode === "ssh_jump") return { mode: "ssh_jump", jump_server_id: Number(draft.jumpServerID) };
  const base = { mode: "socks5" as const, host: normalizedHost(draft.host), port: Number(draft.port) };
  return draft.authType === "none" ? { ...base, auth_type: "none" } : {
    ...base, auth_type: "username_password", username: draft.username,
    ...(draft.password ? { password: draft.password } : {}),
  };
}
export function connectionChanged(draft: ConnectionDraft, saved?: ConnectionConfig): boolean {
  return JSON.stringify(serializeConnection(draft)) !== JSON.stringify(serializeConnection(connectionDraft(saved)));
}
