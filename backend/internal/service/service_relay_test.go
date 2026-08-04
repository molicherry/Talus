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

func TestDefaultExcerptRunes(t *testing.T) {
	if defaultExcerptRunes != 200 {
		t.Errorf("defaultExcerptRunes = %d, want 200", defaultExcerptRunes)
	}
}

func strPtr(s string) *string {
	return &s
}
