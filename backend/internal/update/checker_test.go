package update

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const releaseJSON = `{"tag_name":"v0.13.0","draft":false,"prerelease":false,"published_at":"2026-10-20T12:00:00Z","body":"## Highlights\n- Improved SSH routes"}`

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestNewerStable(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"v0.13.0", "v0.12.0", true},
		{"v0.12.1", "v0.12.0", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.12.0", "v0.9.0", true},
		{"v0.9.0", "v0.12.0", false},
		{"v0.12.0", "v0.12.0", false},
		{"v0.13.0", "v0.13.0-rc.2", true},
		{"v0.13.0", "v0.13.0-test.1", true},
		{"v0.13.0", "v0.14.0-rc.1", false},
		{"v9007199254740993.0.0", "v9007199254740992.0.0", true},
		{"v0.13.0-rc.1", "v0.12.0", false},
		{"v0.13.0-test.1", "v0.12.0", false},
		{"v0.13.0", "dev", false},
		{"v0.13.0", "aabbccd", false},
		{"v0.13.0", "v0.012.0", false},
		{"v0.13.0", "v0.12.0+build.1", false},
		{"v0.13.0", "v0.12.0\n", false},
	} {
		t.Run(tc.latest+"/"+tc.current, func(t *testing.T) {
			if got := newerStable(tc.latest, tc.current); got != tc.want {
				t.Fatalf("newerStable(%q, %q) = %v; want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}

func TestConditionalCheckAndLastKnownResult(t *testing.T) {
	c := NewChecker("v0.12.0")
	calls := 0
	c.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != c.endpoint || r.Header.Get("User-Agent") == "" {
			t.Fatal("expected fixed release endpoint and User-Agent")
		}
		if calls == 1 {
			res := response(http.StatusOK, releaseJSON)
			res.Header.Set("ETag", `"release-13"`)
			return res, nil
		}
		if r.Header.Get("If-None-Match") != `"release-13"` {
			t.Fatal("missing conditional request")
		}
		if calls == 2 {
			return response(http.StatusNotModified, ""), nil
		}
		return response(http.StatusForbidden, "rate limited"), nil
	})
	if err := c.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := c.Snapshot()
	if !first.UpdateAvailable || first.LatestRelease == nil || first.LatestRelease.Version != "v0.13.0" ||
		first.LatestRelease.URL != releasePage+"v0.13.0" || first.CheckedAt == nil {
		t.Fatalf("unexpected status: %+v", first)
	}
	if err := c.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := c.Snapshot()
	if second.CheckedAt.Before(*first.CheckedAt) || !second.UpdateAvailable {
		t.Fatal("304 must retain release information and refresh the check time")
	}
	if err := c.check(context.Background()); err == nil {
		t.Fatal("expected rate-limit error")
	}
	failed := c.Snapshot()
	if !failed.UpdateAvailable || failed.LatestRelease.Version != "v0.13.0" || !failed.CheckedAt.Equal(*second.CheckedAt) {
		t.Fatal("failed checks must preserve the last successful result")
	}
	// API readers cannot mutate the cache and never initiate GitHub requests.
	failed.LatestRelease.Version = "modified"
	*failed.CheckedAt = time.Time{}
	if c.Snapshot().LatestRelease.Version != "v0.13.0" || c.Snapshot().CheckedAt.IsZero() || calls != 3 {
		t.Fatal("snapshot is not an isolated memory-only read")
	}
}

func TestInvalidResponsesKeepCachedRelease(t *testing.T) {
	for _, body := range []string{
		strings.Replace(releaseJSON, `"draft":false`, `"draft":true`, 1),
		strings.Replace(releaseJSON, `"prerelease":false`, `"prerelease":true`, 1),
		strings.Replace(releaseJSON, "v0.13.0", "v0.13.0-rc.1", 1),
		strings.Replace(releaseJSON, "v0.13.0", "v0.13.0-test.1", 1),
		strings.Replace(releaseJSON, "v0.13.0", "v00.13.0", 1),
		strings.Replace(releaseJSON, "2026-10-20T12:00:00Z", "0001-01-01T00:00:00Z", 1),
		`{"tag_name":`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		c := NewChecker("v0.12.0")
		payload := releaseJSON
		c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, payload), nil
		})
		if err := c.check(context.Background()); err != nil {
			t.Fatal(err)
		}
		payload = body
		if err := c.check(context.Background()); err == nil {
			t.Fatal("expected invalid release to be rejected")
		}
		if !c.Snapshot().UpdateAvailable || c.Snapshot().LatestRelease.Version != "v0.13.0" {
			t.Fatal("invalid response replaced valid cached release")
		}
	}
}

func TestNoStableRelease(t *testing.T) {
	c := NewChecker("v0.12.0")
	c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusNotFound, ""), nil
	})
	if err := c.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := c.Snapshot()
	if status.UpdateAvailable || status.LatestRelease != nil || status.CheckedAt == nil {
		t.Fatalf("expected checked status without an update: %+v", status)
	}
}

func TestRunSkipsUnversionedBuilds(t *testing.T) {
	for _, version := range []string{"", "dev", "aabbccd"} {
		c := NewChecker(version)
		c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unversioned build made an outbound request")
			return nil, nil
		})
		c.Run(context.Background())
		if c.Snapshot().CheckedAt != nil {
			t.Fatal("unversioned build claimed to be checked")
		}
	}
}

func TestRunCancellationAndConcurrentReaders(t *testing.T) {
	c := NewChecker("v0.12.0")
	started := make(chan struct{})
	var calls atomic.Int32
	c.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup did not check immediately")
	}
	var readers sync.WaitGroup
	for range 100 {
		readers.Go(func() { c.Snapshot() })
	}
	readers.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the in-flight check")
	}
	if calls.Load() != 1 || c.Snapshot().CheckedAt != nil {
		t.Fatal("readers triggered additional requests or cancellation changed the cache")
	}
}
