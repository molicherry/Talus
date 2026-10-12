package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

// MetricsHandler exposes the metrics query endpoint.
type MetricsHandler struct {
	metricRepo *repository.MetricRepo
}

// NewMetricsHandler creates a MetricsHandler with the given metric repository.
func NewMetricsHandler(metricRepo *repository.MetricRepo) *MetricsHandler {
	return &MetricsHandler{metricRepo: metricRepo}
}

// Get handles GET /api/v1/servers/{id}/metrics.
//
// The response carries the requested and actual granularity, the selected
// statistic, coverage, the data source and whether the series was downsampled
// (REQUIREMENTS §4.2.1). Budget refusals are explicit: 422 with a recommendation
// when a finer granularity cannot fit, 503 when the rollup is not ready or the
// shared deadline expired.
func (h *MetricsHandler) Get(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	if !mw.CheckServerAccess(claims, serverID) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServerDenied))
		return
	}

	request, err := parseMetricQuery(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppErrorParams(http.StatusBadRequest, server.ReasonInvalidQuery, map[string]any{"detail": err.Error()}))
		return
	}
	request.ServerID = serverID

	result, err := h.metricRepo.QueryWithBudget(r.Context(), request)
	if err != nil {
		server.WriteError(w, r, metricQueryError(err))
		return
	}
	if result.Points == nil {
		result.Points = make([]repository.AggregatedMetric, 0)
	}
	server.WriteJSON(w, http.StatusOK, result)
}

// metricQueryError maps a repository refusal onto the documented status.
func metricQueryError(err error) error {
	var budget *repository.QueryBudgetError
	if !errors.As(err, &budget) {
		if errors.Is(err, context.DeadlineExceeded) {
			return server.NewAppError(http.StatusServiceUnavailable, server.ReasonMetricsQueryTimeout)
		}
		return err
	}
	switch budget.Reason {
	case repository.ReasonBucketBudget:
		return server.NewAppErrorParams(http.StatusUnprocessableEntity, server.ReasonMetricsQueryBudget, map[string]any{
			"recommended_interval": budget.Recommended,
		})
	case repository.ReasonRollupNotReady:
		return server.NewAppErrorParams(http.StatusServiceUnavailable, server.ReasonMetricsRollupNotReady, map[string]any{
			"samples": budget.Samples,
		})
	case repository.ReasonQueryTimeout:
		return server.NewAppError(http.StatusServiceUnavailable, server.ReasonMetricsQueryTimeout)
	default:
		return err
	}
}

// parseMetricQuery extracts and validates the query parameters, returning a
// 400-class error for an illegal granularity or an over-30-day range: the range
// is never silently shortened.
func parseMetricQuery(r *http.Request) (repository.QueryRequest, error) {
	q := r.URL.Query()
	now := time.Now().UTC()
	request := repository.QueryRequest{Stat: "avg"}

	from := now.Add(-time.Hour)
	if value := q.Get("from"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return request, errors.New("invalid 'from': must be RFC3339")
		}
		from = parsed
	}
	to := now
	if value := q.Get("to"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return request, errors.New("invalid 'to': must be RFC3339")
		}
		to = parsed
	}
	if !to.After(from) {
		return request, errors.New("'to' must be after 'from'")
	}
	if to.Sub(from) > repository.MaxQueryRange {
		return request, errors.New("range exceeds 30 days")
	}

	interval := q.Get("interval")
	if interval != "" && interval != "auto" && !repository.IsMetricInterval(interval) {
		return request, errors.New("invalid interval: must be auto or one of 1m, 5m, 15m, 1h, 6h")
	}
	if interval == "" {
		interval = "auto"
	}

	stat := q.Get("stat")
	if stat != "" && stat != "avg" && stat != "peak" {
		return request, errors.New("invalid stat: must be avg or peak")
	}
	if stat == "" {
		stat = "avg"
	}

	allowCoarsen := false
	if value := q.Get("allow_coarsen"); value != "" {
		allowCoarsen = value == "true" || value == "1"
	}

	request.From, request.To = from, to
	request.Requested = interval
	request.AllowCoarsen = allowCoarsen
	request.Stat = stat
	return request, nil
}
