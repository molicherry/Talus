package service

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/server"
)

func TestServiceWriteValidation(t *testing.T) {
	svc := NewServiceRelayService(nil, nil)
	for _, tc := range []struct {
		name  string
		input CreateServiceInput
		want  []server.ErrorDetail
	}{
		{
			name: "missing fields",
			want: []server.ErrorDetail{
				server.NewErrorDetail("name", server.ReasonRequired, nil),
				server.NewErrorDetail("base_url", server.ReasonRequired, nil),
				server.NewErrorDetail("credentials", server.ReasonAtLeastOne, nil),
			},
		},
		{
			name: "invalid fields",
			input: CreateServiceInput{
				Name:            "test",
				BaseURL:         "http://:80",
				UsageGuide:      strPtr(strings.Repeat("界", 20001)),
				Credentials:     map[string]string{"b": "", "a": "", "valid": "example"},
				CredentialHints: map[string]string{"valid": "", "unknown.key": "example hint"},
			},
			want: []server.ErrorDetail{
				server.NewErrorDetail("base_url", server.ReasonInvalidBaseURL, nil),
				server.NewErrorDetail("credentials", server.ReasonCredentialValueRequired, map[string]any{"key": "a"}),
				server.NewErrorDetail("credentials", server.ReasonCredentialValueRequired, map[string]any{"key": "b"}),
				server.NewErrorDetail("credential_hints", server.ReasonCredentialHintUnknownKey, map[string]any{"key": "unknown.key"}),
				server.NewErrorDetail("usage_guide", server.ReasonMaxLength, map[string]any{"max": 20000}),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, createErr := svc.Create(t.Context(), tc.input)
			_, updateErr := svc.Update(t.Context(), 1, tc.input)
			for method, err := range map[string]error{"Create": createErr, "Update": updateErr} {
				var validation *server.ValidationError
				if server.StatusCode(err) != http.StatusUnprocessableEntity || !errors.As(err, &validation) {
					t.Fatalf("%s returned %v, want a 422 validation error", method, err)
				}
				if !reflect.DeepEqual(validation.Details, tc.want) {
					t.Errorf("%s details = %+v, want %+v", method, validation.Details, tc.want)
				}
			}
		})
	}
	t.Run("guide at the Unicode character limit", func(t *testing.T) {
		input := CreateServiceInput{
			Name:        "test",
			BaseURL:     "HTTPS://X.EXAMPLE.COM",
			Credentials: map[string]string{"token": "example"},
			UsageGuide:  strPtr(strings.Repeat("界", 20000)),
		}
		if err := input.validate(); err != nil {
			t.Fatal(err)
		}
	})
}

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
	credentials := map[string]string{
		"BACKEND_PATH": "v1//tmp/../jobs/",
		"LABEL":        "a b?#%",
		"TOKEN":        "x%2By",
		"PATH_VALUE":   "a+b",
		"QUERY_VALUE":  "a%2Bb",
		"FIRST":        "{{SECOND}}",
		"SECOND":       "{{FIRST}}",
		"FIRST}}long":  "specific",
	}
	replacer := newRelayCredentialReplacer(credentials)
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
		{"dot segment cleaned", "https://svc.example.com", "/api/../admin", "https://svc.example.com/admin"},
		{"dot segment relative to base subpath", "https://svc.example.com/sub", "/api/../admin", "https://svc.example.com/sub/admin"},
		{"single dot segment removed", "https://svc.example.com", "/api/./admin", "https://svc.example.com/api/admin"},
		{"trailing slash preserved", "https://svc.example.com", "/api/x/", "https://svc.example.com/api/x/"},
		{"encoded dots not treated as segments (URL-escaped, same as JoinPath)", "https://svc.example.com", "/api/%2E%2E/admin", "https://svc.example.com/api/%252E%252E/admin"},
		{"dot segment with query kept", "https://svc.example.com", "/api/../admin?a=1", "https://svc.example.com/admin?a=1"},
		{"path credential with multiple segments", "https://svc.example.com/root", "/{{BACKEND_PATH}}", "https://svc.example.com/root/v1/jobs/"},
		{"reserved characters in path credential", "https://svc.example.com", "/prefix-{{LABEL}}/end", "https://svc.example.com/prefix-a%20b%3F%23%25/end"},
		{"credentials in base path and query", "https://svc.example.com/{{BACKEND_PATH}}?token={{TOKEN}}", "/list", "https://svc.example.com/v1/jobs/list?token=x%2By"},
		{"request query with repeated keys and escapes", "https://svc.example.com?base={{TOKEN}}", "/list?k=%2f&k={{TOKEN}}", "https://svc.example.com/list?k=%2f&k=x%2By"},
		{"separate path and query credentials", "https://svc.example.com", "/x/{{PATH_VALUE}}?v={{QUERY_VALUE}}", "https://svc.example.com/x/a+b?v=a%2Bb"},
		{"percent escape in path credential", "https://svc.example.com", "/x/{{QUERY_VALUE}}", "https://svc.example.com/x/a%252Bb"},
		{"unknown credential in path and query", "https://svc.example.com", "/{{MISSING}}?v={{MISSING}}", "https://svc.example.com/%7B%7BMISSING%7D%7D?v={{MISSING}}"},
		{"replace credentials once in path and query", "https://svc.example.com", "/{{FIRST}}/{{SECOND}}?first={{FIRST}}&second={{SECOND}}", "https://svc.example.com/%7B%7BSECOND%7D%7D/%7B%7BFIRST%7D%7D?first={{SECOND}}&second={{FIRST}}"},
		{"prefer the longer matching placeholder", "https://svc.example.com", "/{{FIRST}}long}}", "https://svc.example.com/specific"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTargetURL(c.baseURL, c.path, replacer)
			if err != nil {
				t.Fatalf("buildTargetURL(%q, %q) error: %v", c.baseURL, c.path, err)
			}
			if got != c.want {
				t.Errorf("buildTargetURL(%q, %q) = %q, want %q", c.baseURL, c.path, got, c.want)
			}
		})
	}
}

func TestBuildTargetURLCollapsesConsecutiveSlashes(t *testing.T) {
	const host = "https://svc.example.com"
	replacer := newRelayCredentialReplacer(map[string]string{"BACKEND_PATH": "a//b"})
	cases := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{"base path", host + "//dbl", "/x", host + "/dbl/x"},
		{"request path", host, "/x//a/b", host + "/x/a/b"},
		{"credential value", host, "/x/{{BACKEND_PATH}}", host + "/x/a/b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTargetURL(c.baseURL, c.path, replacer)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("target URL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildTargetURLInvalidBase(t *testing.T) {
	if _, err := buildTargetURL("://bad", "/api", strings.NewReplacer()); err == nil {
		t.Error("expected error for invalid base URL")
	}
}

func strPtr(s string) *string {
	return &s
}
