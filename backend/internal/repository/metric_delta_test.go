package repository

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// newMetricRepoTestDB creates an isolated schema with the given models migrated.
func newMetricRepoTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate test models: %v", err)
	}
	return db
}

// insertSamples stores explicit metric samples so the delta rules can be
// exercised deterministically.
func insertSamples(t *testing.T, db *gorm.DB, serverID uint, samples []model.Metric) {
	t.Helper()
	for i := range samples {
		samples[i].ServerID = serverID
		if samples[i].Time.IsZero() {
			t.Fatalf("sample %d has no time", i)
		}
		if err := db.Create(&samples[i]).Error; err != nil {
			t.Fatalf("insert sample %d: %v", i, err)
		}
	}
}

func i64(v int64) *int64                           { return &v }
func f64(v float64) *float64                       { return &v }
func t0(base time.Time, d time.Duration) time.Time { return base.Add(d) }

// TestDeltaRatesFollowRequirementRules pins TC-08-03: stable increase, counter
// reset, over-threshold gaps, real zero increments and duplicate timestamps.
func TestDeltaRatesFollowRequirementRules(t *testing.T) {
	db := newMetricRepoTestDB(t, &model.Metric{})
	repo := NewMetricRepo(db)
	ctx := context.Background()

	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	const server = uint(1)
	insertSamples(t, db, server, []model.Metric{
		{Time: t0(base, 0), NetBytesRecv: i64(0)},
		{Time: t0(base, time.Minute), NetBytesRecv: i64(600)},     // +600/60s = 10 B/s
		{Time: t0(base, 2*time.Minute), NetBytesRecv: i64(1200)},  // 10 B/s
		{Time: t0(base, 3*time.Minute), NetBytesRecv: i64(1200)},  // real zero increment
		{Time: t0(base, 4*time.Minute), NetBytesRecv: i64(100)},   // counter reset
		{Time: t0(base, 5*time.Minute), NetBytesRecv: i64(200)},   // recovered after reset
		{Time: t0(base, 5*time.Minute), NetBytesRecv: i64(260)},   // duplicate timestamp
		{Time: t0(base, 15*time.Minute), NetBytesRecv: i64(1260)}, // 10 min gap: excluded
		{Time: t0(base, 16*time.Minute), NetBytesRecv: i64(1860)}, // new pair after the gap
	})

	rows, err := repo.Query(ctx, server, base.Add(-time.Second), base.Add(20*time.Minute), "1 second")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	rates := map[time.Time]*float64{}
	for _, row := range rows {
		rates[row.Time.UTC()] = row.NetRecvRate
	}
	assertRate := func(at time.Duration, want *float64) {
		t.Helper()
		got, ok := rates[t0(base, at)]
		if !ok {
			t.Fatalf("no bucket at %v", at)
		}
		switch {
		case want == nil && got != nil:
			t.Fatalf("rate at %v = %v, want unknown", at, *got)
		case want != nil && got == nil:
			t.Fatalf("rate at %v = unknown, want %v", at, *want)
		case want != nil && got != nil && math.Abs(*got-*want) > 1e-9:
			t.Fatalf("rate at %v = %v, want %v", at, *got, *want)
		}
	}

	// A rate is reported for exactly the seconds its pair covers: the pair that
	// *starts* at a bucket boundary covers that bucket, and the pair that ends
	// exactly there does not (N02 split, per-second buckets here).
	assertRate(0, f64(10))            // covered by the pair 0s -> 60s
	assertRate(time.Minute, f64(10))  // stable increase
	assertRate(2*time.Minute, f64(0)) // the zero increment covers this second
	assertRate(3*time.Minute, nil)    // the reset pair is invalid, so nothing covers it
	assertRate(4*time.Minute, f64(100.0/60.0))
	// Two invalid pairs cover the 5m second and neither yields a rate: the first
	// sample of the duplicate pair has dt=0 (no interval at all), and the pair
	// reaching 15m is beyond the gap threshold.
	assertRate(5*time.Minute, nil)
	assertRate(15*time.Minute, f64(10)) // a fresh pair covers this second
	// The newest sample has no following pair: the last bucket is a gap, not a
	// fabricated rate (the counter's next reading does not exist yet).
	assertRate(16*time.Minute, nil)

	for _, row := range rows {
		if row.NetRecvRate == nil {
			continue
		}
		if math.IsNaN(*row.NetRecvRate) || math.IsInf(*row.NetRecvRate, 0) {
			t.Fatalf("non-finite rate %v in bucket %v", *row.NetRecvRate, row.Time)
		}
	}
}

// TestDeltaGapThresholdIsThreeCollectionIntervals pins the declared value used
// by the acceptance fixtures.
func TestDeltaGapThresholdIsThreeCollectionIntervals(t *testing.T) {
	if DefaultDeltaGapThreshold != 3*time.Minute {
		t.Fatalf("gap threshold = %v, want 3 minutes (3 × the 60s collection interval)", DefaultDeltaGapThreshold)
	}
}
