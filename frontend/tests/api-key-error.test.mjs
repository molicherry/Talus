// Tests the API-key error classifier (compiled by tests/run.sh).
//
// The page must tell a permission problem (403) apart from a rate limit (429)
// and an outage (5xx), and must never treat a missing status as a server
// error — a thrown network failure has no `status`.

import assert from "node:assert/strict";

import { apiKeyErrorKind } from "./.compiled-api-key-error.mjs";

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

const withStatus = (status) => ({ status });

check("401 is unauthorized", () => {
  assert.equal(apiKeyErrorKind(withStatus(401)), "unauthorized");
});

check("403 is forbidden, not unauthorized", () => {
  assert.equal(apiKeyErrorKind(withStatus(403)), "forbidden");
});

check("429 is rateLimited", () => {
  assert.equal(apiKeyErrorKind(withStatus(429)), "rateLimited");
});

check("5xx is a server error", () => {
  assert.equal(apiKeyErrorKind(withStatus(500)), "server");
  assert.equal(apiKeyErrorKind(withStatus(503)), "server");
});

check("other 4xx is unknown", () => {
  assert.equal(apiKeyErrorKind(withStatus(400)), "unknown");
  assert.equal(apiKeyErrorKind(withStatus(404)), "unknown");
  assert.equal(apiKeyErrorKind(withStatus(422)), "unknown");
});

check("no status means a network failure", () => {
  assert.equal(apiKeyErrorKind(new Error("Failed to fetch")), "network");
  assert.equal(apiKeyErrorKind({}), "network");
  assert.equal(apiKeyErrorKind(null), "network");
  assert.equal(apiKeyErrorKind(undefined), "network");
  assert.equal(apiKeyErrorKind("boom"), "network");
});

check("a non-numeric status is not coerced into a server error", () => {
  assert.equal(apiKeyErrorKind({ status: "403" }), "network");
  assert.equal(apiKeyErrorKind({ status: null }), "network");
});

// The backend now sends a stable `reason`; it must win over the status class.
const withReason = (reason, status) => ({ reason, status });

check("api_key_server_denied is forbidden, not unauthorized", () => {
  assert.equal(apiKeyErrorKind(withReason("api_key_server_denied", 403)), "forbidden");
});
check("invalid_credentials is unauthorized", () => {
  assert.equal(apiKeyErrorKind(withReason("invalid_credentials", 401)), "unauthorized");
});
check("rate_limited reason wins over a 5xx status", () => {
  assert.equal(apiKeyErrorKind(withReason("rate_limited", 503)), "rateLimited");
});
check("internal_error is server", () => {
  assert.equal(apiKeyErrorKind(withReason("internal_error", 500)), "server");
});
check("an unknown reason falls back to the status", () => {
  assert.equal(apiKeyErrorKind(withReason("something_new", 403)), "forbidden");
});
check("reason without status is not a network error", () => {
  assert.equal(apiKeyErrorKind(withReason("forbidden", undefined)), "forbidden");
});

console.log(failures === 0 ? "\napi-key-error: ALL PASS" : `\napi-key-error: ${failures} FAILED`);
process.exit(failures === 0 ? 0 : 1);
