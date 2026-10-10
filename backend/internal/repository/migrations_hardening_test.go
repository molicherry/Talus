package repository

import (
	"sync"
	"testing"
)

// TestApplyOnceConcurrentAppliesOnce asserts the transaction-scoped advisory
// lock serializes concurrent callers: the same migration id is applied exactly
// once and every caller observes a consistent result.
func TestApplyOnceConcurrentAppliesOnce(t *testing.T) {
	db := newTestDB(t)
	const id = "test_apply_once_concurrent"
	const sql = `CREATE TABLE apply_once_concurrent (id int)`

	const workers = 8
	var wg sync.WaitGroup
	results := make([]bool, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = ApplyOnce(db, id, sql)
		}(i)
	}
	close(start)
	wg.Wait()

	applied := 0
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("expected exactly one apply across %d concurrent callers, got %d", workers, applied)
	}
}

// TestApplyOnceFailureLeavesNoMarkerAndRetries asserts a failed migration rolls
// back both the SQL and its marker, so a retry applies cleanly and there is no
// partial state.
func TestApplyOnceFailureLeavesNoMarkerAndRetries(t *testing.T) {
	db := newTestDB(t)
	const id = "test_apply_once_failure"

	if _, err := ApplyOnce(db, id, `THIS IS NOT VALID SQL`); err == nil {
		t.Fatal("expected the invalid migration to fail")
	}

	applied, err := ApplyOnce(db, id, `CREATE TABLE apply_once_failure_retry (id int)`)
	if err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if !applied {
		t.Fatal("retry after a failed attempt should apply the migration")
	}

	applied, err = ApplyOnce(db, id, `CREATE TABLE apply_once_failure_retry (id int)`)
	if err != nil {
		t.Fatalf("second retry: %v", err)
	}
	if applied {
		t.Fatal("migration must not re-apply after a successful retry")
	}
}
