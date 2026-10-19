package agentclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
)

func sampleAt(t *testing.T, at time.Time) agentwire.Sample {
	t.Helper()
	id, e := agentwire.NewID()
	if e != nil {
		t.Fatal(e)
	}
	p := 23.0
	return agentwire.Sample{ID: id, BootID: "boot", Time: at, CPU: &p}
}

func TestSpoolSurvivesRestartAndLostAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	q, e := OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := sampleAt(t, time.Now().Add(-time.Minute))
	b := sampleAt(t, time.Now())
	for _, s := range []agentwire.Sample{a, b} {
		if e = q.Add(s); e != nil {
			t.Fatal(e)
		}
	}
	if other, e := OpenSpool(dir); e == nil {
		_ = other.Close()
		t.Fatal("second agent acquired the same outbox")
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	q, e = OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	batch, e := q.Batch()
	if e != nil || len(batch) != 2 || batch[0].ID != b.ID || batch[1].ID != a.ID {
		t.Fatalf("recovered batch: %+v, %v", batch, e)
	}
	if e = q.Ack([]string{b.ID, "../../unrelated"}); e != nil {
		t.Fatal(e)
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	q, e = OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	batch, e = q.Batch()
	if e != nil || len(batch) != 1 || batch[0].ID != a.ID {
		t.Fatalf("ACK removed wrong samples: %+v %v", batch, e)
	}
}

func TestSpoolBoundsAndPersistentLossCount(t *testing.T) {
	dir := t.TempDir()
	q, e := OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	a := sampleAt(t, now.Add(-time.Hour))
	b := sampleAt(t, now)
	if e = q.Add(a); e != nil {
		t.Fatal(e)
	}
	q.limit = q.bytes + 10
	if e = q.Add(b); e != nil {
		t.Fatal(e)
	}
	count, lost := q.Stats()
	if count != 1 || lost != 1 {
		t.Fatalf("bounds: %d %d", count, lost)
	}
	q.now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, e = q.Batch(); e != nil {
		t.Fatal(e)
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	q, e = OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	count, lost = q.Stats()
	if count != 0 || lost != 2 {
		t.Fatalf("persisted losses: %d %d", count, lost)
	}
	if e = os.WriteFile(filepath.Join(dir, ".pending-interrupted"), []byte("partial"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	q, e = OpenSpool(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if _, e = os.Stat(filepath.Join(dir, ".pending-interrupted")); !os.IsNotExist(e) {
		t.Fatal("interrupted temp file not cleaned")
	}
}

func TestSpoolRejectedCaptureTimesCountAsLossAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := OpenSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, b := sampleAt(t, time.Now()), sampleAt(t, time.Now())
	for _, s := range []agentwire.Sample{a, b} {
		if err = q.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	if err = q.Reject([]string{a.ID, a.ID, "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = OpenSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if count, dropped := q.Stats(); count != 1 || dropped != 1 {
		t.Fatalf("rejected sample loss not durable: %d %d", count, dropped)
	}
	batch, err := q.Batch()
	if err != nil || len(batch) != 1 || batch[0].ID != b.ID {
		t.Fatalf("rejection removed valid observation: %+v %v", batch, err)
	}
}
