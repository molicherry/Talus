package agentwire

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestRejectInvalidMetricsAndCredentialURLs(t *testing.T) {
	for _, url := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?token=abc", "https://example.com/#x", "file:///tmp/hub"} {
		if (Config{HubURL: url, Token: strings.Repeat("a", 64), StateDir: "/state", IntervalSeconds: 5}).Validate() == nil {
			t.Fatalf("accepted URL %s", url)
		}
	}
	id, _ := NewID()
	s := Sample{ID: id, BootID: "boot", Time: time.Now()}
	if e := s.Validate(time.Now()); e != nil {
		t.Fatal(e)
	}
	p := math.NaN()
	s.CPU = &p
	if s.Validate(time.Now()) == nil {
		t.Fatal("accepted NaN")
	}
	s.CPU = nil
	s.Time = time.Now().Add(time.Hour)
	if s.Validate(time.Now()) == nil {
		t.Fatal("accepted future sample")
	}
}
