import { z } from "zod";
import i18n from "../i18n";

export const ServerSchema = z.object({
  id: z.number(),
  name: z.string(),
  host: z.string(),
  port: z.number().min(1).max(65535),
  description: z.string().nullable().optional(),
  notes: z.string().nullable().optional(),
  owner_id: z.number(),
  created_at: z.string(),
  updated_at: z.string().optional(),
  status: z.enum(["online", "offline", "checking", "unknown"]).optional(),
  last_seen: z.string().optional(),
  latest_metrics: z
    .object({
      cpu_percent: z.number().nullable().optional(),
      memory_percent: z.number().nullable().optional(),
      disk_percent: z.number().nullable().optional(),
    })
    .nullable()
    .optional(),
  os: z.string().nullable().optional(),
  cpu_model: z.string().nullable().optional(),
  uptime_seconds: z.number().nullable().optional(),
  credential_id: z.number().nullable().optional(),
  credential: z
    .object({
      id: z.number(),
      name: z.string().nullable().optional(),
      auth_type: z.string(),
      username: z.string(),
    })
    .nullable()
    .optional(),
});
export type Server = z.infer<typeof ServerSchema>;

export const ServerFormSchema = z.object({
  name: z.string().min(1, i18n.t("validation.nameRequired")).max(128),
  host: z.string().min(1, i18n.t("validation.hostRequired")).max(256),
  port: z
    .number({ message: i18n.t("validation.portRequired") })
    .min(1)
    .max(65535),
  description: z.string().max(500).optional(),
  notes: z.string().max(2000).optional(),
  credential_id: z.number().nullable().optional(),
});
export type ServerFormValues = z.infer<typeof ServerFormSchema>;

export const SSHCredentialSchema = z.object({
  id: z.number(),
  name: z.string().nullable().optional(),
  auth_type: z.enum(["password", "private_key"]),
  username: z.string(),
  key_fingerprint: z.string().nullable().optional(),
  created_at: z.string(),
});
export type SSHCredential = z.infer<typeof SSHCredentialSchema>;

export const CredentialFormSchema = z
  .object({
    name: z.string().max(128).optional(),
    auth_type: z.enum(["password", "private_key"], {
      message: i18n.t("validation.authTypeRequired"),
    }),
    username: z.string().min(1, i18n.t("validation.usernameRequired")),
    password: z.string().optional(),
    private_key: z.string().optional(),
  })
  .refine((data) => (data.auth_type === "password" ? !!data.password : !!data.private_key), {
    message: i18n.t("validation.credentialRequired"),
    path: ["password"],
  });
export type CredentialFormValues = z.infer<typeof CredentialFormSchema>;
