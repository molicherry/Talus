package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
)

// TestMetricQueryErrorMapping pins TC-04-05's HTTP semantics: an over-budget
// granularity is a 422 carrying the recommendation, a missing rollup and an
// expired deadline are 503s with their own reasons.
func TestMetricQueryErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{
			"bucket budget carries a recommendation",
			&repository.QueryBudgetError{Reason: repository.ReasonBucketBudget, Recommended: "6h"},
			http.StatusUnprocessableEntity, server.ReasonMetricsQueryBudget,
		},
		{
			"rollup not ready",
			&repository.QueryBudgetError{Reason: repository.ReasonRollupNotReady, Samples: 12001},
			http.StatusServiceUnavailable, server.ReasonMetricsRollupNotReady,
		},
		{
			"query timeout",
			&repository.QueryBudgetError{Reason: repository.ReasonQueryTimeout},
			http.StatusServiceUnavailable, server.ReasonMetricsQueryTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := metricQueryError(tc.err)
			appErr, ok := err.(*server.AppError)
			if !ok {
				t.Fatalf("err = %T, want an AppError", err)
			}
			if appErr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", appErr.Code, tc.wantStatus)
			}
			if appErr.Reason != tc.wantReason {
				t.Fatalf("reason = %s, want %s", appErr.Reason, tc.wantReason)
			}
		})
	}
}

// TestMetricQueryParameterValidation pins the 400-class rules: an illegal
// granularity or a range beyond 30 days is rejected, never silently shortened.
func TestMetricQueryParameterValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := "from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Format(time.RFC3339)

	cases := []struct {
		name  string
		query string
		ok    bool
	}{
		{"defaults are auto/avg", valid, true},
		{"auto interval", valid + "&interval=auto", true},
		{"every legal granularity", valid + "&interval=6h", true},
		{"peak statistic", valid + "&stat=peak", true},
		{"allow_coarsen", valid + "&interval=1h&allow_coarsen=true", true},
		{"illegal interval", valid + "&interval=2h", false},
		{"illegal stat", valid + "&stat=median", false},
		{"to before from", "from=" + now.Format(time.RFC3339) + "&to=" + now.Add(-time.Hour).Format(time.RFC3339), false},
		{"range beyond 30 days", "from=" + now.Add(-31*24*time.Hour).Format(time.RFC3339) + "&to=" + now.Format(time.RFC3339), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/servers/1/metrics?"+tc.query, nil)
			parsed, err := parseMetricQuery(request)
			if tc.ok && err != nil {
				t.Fatalf("valid query rejected: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("invalid query accepted: %+v", parsed)
				}
				return
			}
			if parsed.Requested == "" || parsed.Stat == "" {
				t.Fatalf("parsed request is incomplete: %+v", parsed)
			}
		})
	}
}

// TestMetricQueryErrorPassesThroughUnknownErrors keeps non-budget failures intact
// so a database fault is not disguised as a budget refusal.
func TestMetricQueryErrorPassesThroughUnknownErrors(t *testing.T) {
	sentinel := errors.New("boom")
	if got := metricQueryError(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("err = %v, want the original error", got)
	}
}

// TestReviewDocumentedStatMaxAccepted pins R20: the frozen `stat=max` name is
// accepted as an alias of the API's `peak` spelling, and unknown values are still
// rejected.
func TestReviewDocumentedStatMaxAccepted(t *testing.T) {
	now := time.Now().UTC()
	valid := "from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Format(time.RFC3339)

	for _, tc := range []struct {
		query  string
		want   string
		echo   string
		reason string
	}{
		{"&stat=max", "peak", "max", ""},
		{"&stat=peak", "peak", "", ""},
		{"&stat=avg", "avg", "", ""},
		{"", "avg", "", ""},
		{"&stat=median", "", "", "invalid stat"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/servers/1/metrics?"+valid+tc.query, nil)
		parsed, err := parseMetricQuery(request)
		if tc.reason != "" {
			if err == nil {
				t.Fatalf("%q: expected a rejection", tc.query)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.query, err)
		}
		if parsed.Stat != tc.want {
			t.Fatalf("%q: stat = %q, want %q", tc.query, parsed.Stat, tc.want)
		}
		if parsed.EchoStat != tc.echo {
			t.Fatalf("%q: echo = %q, want %q", tc.query, parsed.EchoStat, tc.echo)
		}
	}
}
