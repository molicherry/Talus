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

// TestApplyOnceRollsBackPartialWorkOnFailure proves the transaction rolls back
// work already written (not just a marker) when the migration fails midway.
func TestApplyOnceRollsBackPartialWorkOnFailure(t *testing.T) {
	db := newTestDB(t)
	const id = "test_apply_once_partial"
	// Creates a table, then raises: both the table and the marker must roll back.
	partial := `DO $$ BEGIN CREATE TABLE apply_once_partial (id int); RAISE EXCEPTION 'boom'; END $$;`
	if _, err := ApplyOnce(db, id, partial); err == nil {
		t.Fatal("expected the partial migration to fail")
	}
	if db.Migrator().HasTable("apply_once_partial") {
		t.Fatal("work written before the failure must be rolled back")
	}
	applied, err := ApplyOnce(db, id, `CREATE TABLE apply_once_partial (id int)`)
	if err != nil {
		t.Fatalf("retry after a partial failure: %v", err)
	}
	if !applied {
		t.Fatal("retry after a partial failure should apply the migration")
	}
}

// TestApplyOnceRollsBackWhenMarkerWriteFails proves the outer transaction covers
// both the migration SQL and its marker: when the marker insert fails after the
// SQL has already succeeded, the SQL is rolled back too.
func TestApplyOnceRollsBackWhenMarkerWriteFails(t *testing.T) {
	db := newTestDB(t)
	const id = "test_apply_once_marker_fail"
	if err := db.Exec(createMigrationsTableSQL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE OR REPLACE FUNCTION talus_marker_boom() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'marker boom'; END $$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER talus_marker_boom BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION talus_marker_boom()`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyOnce(db, id, `CREATE TABLE marker_fail_work (id int)`); err == nil {
		t.Fatal("expected the marker write to fail")
	}
	if db.Migrator().HasTable("marker_fail_work") {
		t.Fatal("SQL work must roll back when the marker write fails")
	}
	if err := db.Exec(`DROP TRIGGER talus_marker_boom ON schema_migrations`).Error; err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOnce(db, id, `CREATE TABLE marker_fail_work (id int)`)
	if err != nil {
		t.Fatalf("retry after marker failure: %v", err)
	}
	if !applied {
		t.Fatal("retry should apply once the marker can be written")
	}
}
