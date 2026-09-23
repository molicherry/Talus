import assert from "node:assert/strict";
import {
  createCredentialRow,
  credentialRowsFromValues,
  credentialRowsToValues,
} from "./.compiled-credential-rows.mjs";

const rows = credentialRowsFromValues(
  { token: "first-example", token2: "second-example" },
  { token: "first hint", token2: "second hint" },
);
const collision = rows.map((row, index) => (index === 1 ? { ...row, key: "token" } : row));
assert.equal(collision.length, 2);
assert.deepEqual(credentialRowsToValues(collision), {
  ok: false,
  error: { code: "duplicateKey", rowId: rows[1].id },
});
assert.equal(collision[0].value, "first-example");
assert.equal(collision[1].value, "second-example");
const renamed = collision.map((row, index) => (index === 1 ? { ...row, key: "renamed" } : row));
assert.equal(renamed[1].id, rows[1].id);
assert.deepEqual(credentialRowsToValues(renamed), {
  ok: true,
  credentials: { token: "first-example", renamed: "second-example" },
  credential_hints: { token: "first hint", renamed: "second hint" },
});
const blanks = [createCredentialRow(), createCredentialRow()];
assert.notEqual(blanks[0].id, blanks[1].id);
assert.equal(credentialRowsToValues(blanks).error.code, "missingKey");
assert.equal(credentialRowsToValues([]).error.code, "empty");
assert.equal(
  credentialRowsToValues([createCredentialRow("   ", "value")]).error.code,
  "missingKey",
);
assert.equal(credentialRowsToValues([createCredentialRow("token")]).error.code, "missingValue");
const special = credentialRowsToValues([createCredentialRow("__proto__", "literal", "hint")]);
assert.equal(special.ok, true);
assert.equal(Object.hasOwn(special.credentials, "__proto__"), true);
assert.equal(JSON.parse(JSON.stringify(special.credentials)).__proto__, "literal");
const restored = credentialRowsFromValues(JSON.parse('{"__proto__":"literal","toString":"other"}'));
assert.deepEqual(
  restored.map((row) => row.hint),
  ["", ""],
);
console.log("Credential draft collisions, independent rows, validation and serialization passed.");
