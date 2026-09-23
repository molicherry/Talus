export interface CredentialRow {
  id: string;
  key: string;
  value: string;
  hint: string;
}

let nextRowId = 0;

export function createCredentialRow(key = "", value = "", hint = ""): CredentialRow {
  // Also works on HTTP deployments where crypto.randomUUID is unavailable.
  return { id: `service-key-${++nextRowId}`, key, value, hint };
}

export function credentialRowsFromValues(
  credentials: Record<string, string>,
  hints: Record<string, string> = {},
): CredentialRow[] {
  return Object.entries(credentials).map(([key, value]) =>
    createCredentialRow(key, value, Object.hasOwn(hints, key) ? hints[key] : ""),
  );
}

export type CredentialRowsError = {
  code: "empty" | "missingKey" | "missingValue" | "duplicateKey";
  rowId?: string;
};

/** Convert only at submission: an object cannot represent duplicate draft keys. */
export function credentialRowsToValues(
  rows: CredentialRow[],
):
  | { ok: true; credentials: Record<string, string>; credential_hints: Record<string, string> }
  | { ok: false; error: CredentialRowsError } {
  if (rows.length === 0) return { ok: false, error: { code: "empty" } };
  const keys = new Set<string>();
  for (const row of rows) {
    if (!row.key.trim()) return { ok: false, error: { code: "missingKey", rowId: row.id } };
    if (keys.has(row.key)) return { ok: false, error: { code: "duplicateKey", rowId: row.id } };
    if (!row.value) return { ok: false, error: { code: "missingValue", rowId: row.id } };
    keys.add(row.key);
  }
  // fromEntries also preserves literal keys such as "__proto__" safely.
  return {
    ok: true,
    credentials: Object.fromEntries(rows.map((row) => [row.key, row.value])),
    credential_hints: Object.fromEntries(rows.map((row) => [row.key, row.hint])),
  };
}
