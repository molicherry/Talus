package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
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
func (h *MetricsHandler) Get(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	from, to, pgInterval, err := parseMetricParams(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, err.Error()))
		return
	}

	results, err := h.metricRepo.Query(r.Context(), serverID, from, to, pgInterval)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}

	if results == nil {
		results = make([]repository.AggregatedMetric, 0)
	}

	server.WriteJSON(w, http.StatusOK, results)
}

// parseMetricParams extracts and validates query parameters for the metrics endpoint.
func parseMetricParams(r *http.Request) (from, to time.Time, interval string, err error) {
	q := r.URL.Query()

	now := time.Now().UTC()

	// Parse from
	if fromStr := q.Get("from"); fromStr != "" {
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
	} else {
		from = now.Add(-1 * time.Hour)
	}

	// Parse to
	if toStr := q.Get("to"); toStr != "" {
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
	} else {
		to = now
	}

	if !to.After(from) {
		return time.Time{}, time.Time{}, "", errors.New("'to' must be after 'from'")
	}

	// Determine interval
	if intervalStr := q.Get("interval"); intervalStr != "" {
		switch intervalStr {
		case "1m":
			interval = "1 minute"
		case "5m":
			interval = "5 minutes"
		case "15m":
			interval = "15 minutes"
		case "1h":
			interval = "1 hour"
		default:
			return time.Time{}, time.Time{}, "", errors.New("invalid interval: must be one of 1m, 5m, 15m, 1h")
		}
	} else {
		interval = defaultInterval(from, to)
	}

	return from, to, interval, nil
}

// defaultInterval returns an appropriate PostgreSQL interval string based on
// the time range duration.
func defaultInterval(from, to time.Time) string {
	dur := to.Sub(from)
	switch {
	case dur <= 1*time.Hour:
		return "1 minute"
	case dur <= 6*time.Hour:
		return "5 minutes"
	case dur <= 24*time.Hour:
		return "15 minutes"
	default:
		return "1 hour"
	}
}
