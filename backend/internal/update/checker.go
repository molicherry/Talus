// Package update checks the public Talus release feed without blocking API requests.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const checkInterval = 6 * time.Hour
const releasePage = "https://github.com/molicherry/Talus/releases/tag/"

// Match the repository's stable/RC/test tag policy, including arbitrary-size numbers.
var tagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(rc|test)\.([1-9][0-9]*))?$`)

type Release struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
}

type Status struct {
	CurrentVersion  string     `json:"current_version"`
	LatestRelease   *Release   `json:"latest_release"`
	UpdateAvailable bool       `json:"update_available"`
	CheckedAt       *time.Time `json:"checked_at"`
}

type Checker struct {
	mu       sync.RWMutex
	status   Status
	etag     string
	client   *http.Client
	endpoint string
}

func NewChecker(version string) *Checker {
	return &Checker{
		status:   Status{CurrentVersion: version},
		client:   &http.Client{Timeout: 5 * time.Second},
		endpoint: "https://api.github.com/repos/molicherry/Talus/releases/latest",
	}
}

// Snapshot only reads memory. A slow or unavailable GitHub never delays the UI.
func (c *Checker) Snapshot() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := c.status
	if snapshot.LatestRelease != nil {
		release := *snapshot.LatestRelease
		snapshot.LatestRelease = &release
	}
	if snapshot.CheckedAt != nil {
		checked := *snapshot.CheckedAt
		snapshot.CheckedAt = &checked
	}
	return snapshot
}

// Run has a single writer, checks immediately, and stops with the application.
// Development/hash builds have no comparable version and make no outbound request.
func (c *Checker) Run(ctx context.Context) {
	if tagPattern.FindStringSubmatch(c.status.CurrentVersion) == nil {
		return
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.check(ctx); err != nil && ctx.Err() == nil {
			slog.Debug("release update check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Checker) check(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "Talus-update-checker")
	if c.etag != "" {
		request.Header.Set("If-None-Match", c.etag)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	checked := time.Now().UTC()
	switch response.StatusCode {
	case http.StatusNotModified:
		c.mu.Lock()
		c.status.CheckedAt = &checked
		c.mu.Unlock()
		return nil
	case http.StatusNotFound:
		// GitHub returns 404 when the repository has no published stable release.
		c.mu.Lock()
		c.status.LatestRelease = nil
		c.status.UpdateAvailable = false
		c.status.CheckedAt = &checked
		c.mu.Unlock()
		c.etag = ""
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("GitHub release check returned HTTP %d", response.StatusCode)
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("GitHub release response exceeds size limit")
	}
	var release struct {
		Tag         string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Body        string    `json:"body"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return err
	}
	version := tagPattern.FindStringSubmatch(release.Tag)
	if version == nil || version[4] != "" || release.Draft || release.Prerelease || release.PublishedAt.IsZero() {
		return fmt.Errorf("GitHub returned an invalid stable release")
	}
	c.mu.Lock()
	c.status.LatestRelease = &Release{
		Version: release.Tag, PublishedAt: release.PublishedAt,
		Notes: release.Body, URL: releasePage + release.Tag,
	}
	c.status.UpdateAvailable = newerStable(release.Tag, c.status.CurrentVersion)
	c.status.CheckedAt = &checked
	c.mu.Unlock()
	c.etag = response.Header.Get("ETag")
	return nil
}

func newerStable(latest, current string) bool {
	a, b := tagPattern.FindStringSubmatch(latest), tagPattern.FindStringSubmatch(current)
	if a == nil || b == nil || a[4] != "" {
		return false
	}
	for i := 1; i <= 3; i++ {
		if len(a[i]) != len(b[i]) {
			return len(a[i]) > len(b[i])
		}
		if order := strings.Compare(a[i], b[i]); order != 0 {
			return order > 0
		}
	}
	// A stable release supersedes an RC/test of the same base version.
	return b[4] != ""
}
