package server

import (
	"os"
	"regexp"
	"testing"
)

// The const block and reasonMessages are two hand-maintained lists that must
// stay in lockstep: a reason without a message would ship an empty English
// fallback, and a message without a reason is either dead or a typo. This test
// reads this very file so the two lists cannot drift silently.
func TestEveryReasonHasEnglishMessage(t *testing.T) {
	src, err := os.ReadFile("errors.go")
	if err != nil {
		t.Fatalf("read errors.go: %v", err)
	}
	text := string(src)

	decl := regexp.MustCompile(`(?m)^\t(Reason\w+)\s+=\s+"([^"]+)"`)
	declared := map[string]string{} // identifier -> value
	for _, m := range decl.FindAllStringSubmatch(text, -1) {
		declared[m[1]] = m[2]
	}
	if len(declared) == 0 {
		t.Fatal("no reason constants found in errors.go")
	}

	// Entries in the map are keyed by the constant identifier, not the literal.
	mapBody := regexp.MustCompile(`(?s)var reasonMessages = map\[string\]string\{(.*?)\n\}`).FindStringSubmatch(text)
	if mapBody == nil {
		t.Fatal("reasonMessages map not found in errors.go")
	}
	keyed := regexp.MustCompile(`(?m)^\t(Reason\w+):\s+"`).FindAllStringSubmatch(mapBody[1], -1)
	inMap := map[string]bool{}
	for _, m := range keyed {
		inMap[m[1]] = true
	}

	for id := range declared {
		if !inMap[id] {
			t.Errorf("%s (%q) has no entry in reasonMessages", id, declared[id])
		}
	}
	for id := range inMap {
		if _, ok := declared[id]; !ok {
			t.Errorf("reasonMessages has %s, but no such constant is declared", id)
		}
	}
}

// A client cannot translate a reason it never receives, so the wire envelope
// must always carry one.
func TestMarshalErrorIncludesReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"sentinel", ErrForbidden, ReasonForbidden},
		{"constructed", NewAppError(400, ReasonInvalidServerID), ReasonInvalidServerID},
		{"validation", NewValidationError([]ErrorDetail{NewErrorDetail("host", ReasonRequired, nil)}), ReasonValidationFailed},
		{"plain error", os.ErrNotExist, ReasonInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := string(marshalError(c.err, "req-1"))
			if !regexp.MustCompile(`"reason":"` + c.want + `"`).MatchString(body) {
				t.Errorf("reason %q missing from %s", c.want, body)
			}
		})
	}
}

// Field details carry their own reason and params so the client can localise the
// field label and the numbers.
func TestValidationDetailCarriesReasonAndParams(t *testing.T) {
	body := string(marshalError(NewValidationError([]ErrorDetail{
		NewErrorDetail("password", ReasonLength, map[string]any{"min": 8, "max": 64}),
	}), "req-2"))
	for _, want := range []string{`"field":"password"`, `"reason":"length"`, `"min":8`, `"max":64`} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(body) {
			t.Errorf("%s missing from %s", want, body)
		}
	}
}
