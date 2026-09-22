/**
 * Decides what a 401 means.
 *
 * Usually a 401 says the session is gone, so the api client clears the token and
 * sends the user to the login page. Two 401s are different: they are the *result*
 * of a credential the user just typed, and they belong inline on the form that
 * asked for it — bouncing to /login would throw away what they were doing.
 *
 *   - invalid_credentials         the login form
 *   - current_password_incorrect  the change-password dialog
 *
 * The backend sends these as stable `reason`s (see api-error.ts); anything else,
 * including a 401 with no reason at all, is treated as an expired session.
 *
 * Kept dependency-free so tests/run.sh can compile it on its own.
 */
const CREDENTIAL_REASONS = new Set(["invalid_credentials", "current_password_incorrect"]);

export function isCredentialRejection(reason: unknown): boolean {
  return typeof reason === "string" && CREDENTIAL_REASONS.has(reason);
}
