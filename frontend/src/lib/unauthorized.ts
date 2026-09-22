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

/**
 * What to do about a 401.
 *
 * - `inline`                    the user just mistyped a credential: keep the
 *                               form and its message, and do not touch the token
 * - `clear-token`               the session is gone, but we are already on the
 *                               login route. Clearing is enough — navigating
 *                               there again reloads the page, and /login probes
 *                               `/api/v1/auth/setup` on every load, so a proxy
 *                               that keeps answering 401 on that path would
 *                               reload the login page forever
 * - `clear-token-and-redirect`  the session is gone somewhere else
 */
export type UnauthorizedAction = "inline" | "clear-token" | "clear-token-and-redirect";

/** True when the given pathname is the login route (leading base paths and a trailing slash are fine). */
export function isLoginRoute(pathname: string): boolean {
  return pathname.replace(/\/+$/, "").endsWith("/login");
}

export function classifyUnauthorized(reason: unknown, pathname: string): UnauthorizedAction {
  if (isCredentialRejection(reason)) return "inline";
  return isLoginRoute(pathname) ? "clear-token" : "clear-token-and-redirect";
}
