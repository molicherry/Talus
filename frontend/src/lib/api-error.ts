import i18n from "../i18n";
import { ApiClientError } from "./api-client";

type TranslateFn = (key: string, params?: Record<string, string | number>) => string;

/** i18n key for a backend reason: `invalid_server_id` → `errors.invalid_server_id`. */
export function reasonKey(reason?: string): string | undefined {
  return reason ? `errors.${reason}` : undefined;
}

/**
 * Localized text for anything a request can throw.
 *
 * The backend attaches a stable `reason` to every error and the locales mirror
 * it as `errors.<reason>`, so the server's English `message` never has to reach
 * the UI. A reason the client does not know yet falls back to the localized
 * "request failed with status N" instead of leaking that English text.
 *
 * Pass the caller's own contextual key as `fallbackKey` for errors that carry
 * no API information at all.
 */
export function translateApiError(
  err: unknown,
  t: TranslateFn,
  fallbackKey = "common.unexpectedError",
): string {
  if (err instanceof ApiClientError) {
    const key = reasonKey(err.reason);
    if (key && i18n.has(key)) return t(key, err.params);
    return t("common.apiError", { status: err.status });
  }
  // fetch() rejects with a TypeError when the request never reaches the server.
  if (err instanceof TypeError) return t("common.networkError");
  return t(fallbackKey);
}

/**
 * Field-level messages from a validation error (422), grouped by field name.
 *
 * A field can carry several details at once: `credentials` reports one entry per
 * empty value, so callers that render a single string must decide how to join
 * them instead of silently keeping the last one.
 */
export function apiFieldErrorList(err: unknown, t: TranslateFn): Record<string, string[]> {
  if (!(err instanceof ApiClientError)) return {};
  // Null prototype: a field literally named "toString" must allocate its own
  // list, not resolve to the inherited method and fail on push.
  const out = Object.create(null) as Record<string, string[]>;
  for (const detail of err.details ?? []) {
    const label = i18n.has(`fields.${detail.field}`) ? t(`fields.${detail.field}`) : detail.field;
    const key = reasonKey(detail.reason);
    const message =
      key && i18n.has(key) ? t(key, { ...detail.params, field: label }) : t("errors.validation_failed");
    (out[detail.field] ??= []).push(message);
  }
  return out;
}

/**
 * Field-level messages from a validation error (422), keyed by field name so a
 * form can drop them straight into its per-field error state. The field name is
 * translated through `fields.<name>` when a label exists. Fields with several
 * details are joined into one string.
 */
export function apiFieldErrors(err: unknown, t: TranslateFn): Record<string, string> {
  const out = Object.create(null) as Record<string, string>;
  for (const [field, messages] of Object.entries(apiFieldErrorList(err, t))) {
    out[field] = messages.join(" ");
  }
  return out;
}
