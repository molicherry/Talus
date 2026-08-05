package service

import (
	"strings"
	"testing"
)

func TestExcerpt(t *testing.T) {
	cases := []struct {
		name string
		in   *string
		max  int
		want string
	}{
		{"nil", nil, 200, ""},
		{"empty", strPtr(""), 200, ""},
		{"short", strPtr("hello"), 200, "hello"},
		{"exact boundary", strPtr("hello"), 5, "hello"},
		{"truncated", strPtr("hello world"), 5, "hello…"},
		{"max zero", strPtr("hello"), 0, "…"},
		{"utf8 not cut mid-codepoint", strPtr("你好，世界！"), 3, "你好，" + "…"},
		{"200 rune boundary keeps 200", strPtr(strings.Repeat("a", 200)), 200, strings.Repeat("a", 200)},
		{"201 rune truncates to 200 + ellipsis", strPtr(strings.Repeat("a", 201)), 200, strings.Repeat("a", 200) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := excerpt(c.in, c.max); got != c.want {
				t.Errorf("excerpt(%v, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
		})
	}
}

func TestBuildTargetURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{"plain path", "https://svc.example.com", "/api/x", "https://svc.example.com/api/x"},
		{"trailing slash base", "https://svc.example.com/", "/api/x", "https://svc.example.com/api/x"},
		{"query preserved", "https://svc.example.com/", "/api/x?a=b", "https://svc.example.com/api/x?a=b"},
		{"encoded query preserved", "https://svc.example.com/", "/api/compose.one?input=%7B%22json%22%3A%7B%22composeId%22%3A%22abc%22%7D%7D", "https://svc.example.com/api/compose.one?input=%7B%22json%22%3A%7B%22composeId%22%3A%22abc%22%7D%7D"},
		{"multi-param query", "https://svc.example.com/", "/api/list?a=1&b=2", "https://svc.example.com/api/list?a=1&b=2"},
		{"empty path", "https://svc.example.com/", "", "https://svc.example.com/"},
		{"root path", "https://svc.example.com/", "/", "https://svc.example.com/"},
		{"base subpath", "https://svc.example.com/sub", "/api/x", "https://svc.example.com/sub/api/x"},
		{"query without path", "https://svc.example.com/", "?a=1", "https://svc.example.com/?a=1"},
		{"no leading slash path", "https://svc.example.com/", "api/x", "https://svc.example.com/api/x"},
		{"base query preserved", "https://svc.example.com/?token=abc", "/api/x", "https://svc.example.com/api/x?token=abc"},
		{"path query wins over base query", "https://svc.example.com/?token=abc", "/api/x?a=1", "https://svc.example.com/api/x?a=1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTargetURL(c.baseURL, c.path)
			if err != nil {
				t.Fatalf("buildTargetURL(%q, %q) error: %v", c.baseURL, c.path, err)
			}
			if got != c.want {
				t.Errorf("buildTargetURL(%q, %q) = %q, want %q", c.baseURL, c.path, got, c.want)
			}
		})
	}
}

func TestBuildTargetURLInvalidBase(t *testing.T) {
	if _, err := buildTargetURL("://bad", "/api"); err == nil {
		t.Error("expected error for invalid base URL")
	}
}

func strPtr(s string) *string {
	return &s
}
