// Tests the 401 classifier compiled by tests/run.sh.
//
// The api client clears the token and redirects on a 401 — except when the 401
// is the result of a credential the user just typed, which must stay inline on
// the form (otherwise mistyping your current password throws you back to /login).

import assert from "node:assert/strict";

import { isCredentialRejection } from "./.compiled-unauthorized.mjs";

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`  ok: ${name}`);
  } catch (err) {
    failures++;
    console.log(`  FAIL: ${name}\n    ${err.message}`);
  }
}

check("a wrong password on the login form stays inline", () =>
  assert.equal(isCredentialRejection("invalid_credentials"), true));
check("a wrong current password stays in the dialog", () =>
  assert.equal(isCredentialRejection("current_password_incorrect"), true));
check("an expired session redirects", () =>
  assert.equal(isCredentialRejection("unauthorized"), false));
check("a missing reason redirects (proxy 401, dropped token)", () =>
  assert.equal(isCredentialRejection(undefined), false));
check("an unknown reason redirects", () =>
  assert.equal(isCredentialRejection("something_new"), false));
check("a non-string reason redirects", () =>
  assert.equal(isCredentialRejection(401), false));

console.log(failures === 0 ? "\nunauthorized: ALL PASS" : `\nunauthorized: ${failures} FAILED`);
process.exit(failures === 0 ? 0 : 1);
