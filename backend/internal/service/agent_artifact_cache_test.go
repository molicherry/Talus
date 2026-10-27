package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/repository"
)

type countedArtifactFile struct {
	file  *os.File
	reads int
}

func (f *countedArtifactFile) Read(p []byte) (int, error) {
	n, err := f.file.Read(p)
	f.reads += n
	return n, err
}
func (f *countedArtifactFile) Seek(n int64, whence int) (int64, error) {
	return f.file.Seek(n, whence)
}
func (f *countedArtifactFile) Stat() (os.FileInfo, error) { return f.file.Stat() }

func TestAgentArtifactVerificationCachesAndInvalidatesFileMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	data := bytes.Repeat([]byte("verified-artifact"), 1024)
	a := agentproto.Artifact{Size: int64(len(data)), SHA256: repository.TokenHash(string(data))}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var cache agentBundleCache
	verify := func() (int, error) {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		counted := &countedArtifactFile{file: f}
		_, err = cache.verify(counted, path, a)
		return counted.reads, err
	}
	if reads, err := verify(); err != nil || reads != len(data) {
		t.Fatalf("initial verification: reads=%d, err=%v", reads, err)
	}
	for i := 0; i < 3; i++ {
		if reads, err := verify(); err != nil || reads != 0 {
			t.Fatalf("unchanged Range/HEAD repeated full hashing: reads=%d, err=%v", reads, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Replacing the inode must invalidate even when size and mtime are equal.
	replacement := path + ".replacement"
	if err := os.WriteFile(replacement, bytes.Repeat([]byte("x"), len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if reads, err := verify(); err == nil || reads != len(data) {
		t.Fatalf("replacement reused stale verification: reads=%d, err=%v", reads, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verify(); err != nil {
		t.Fatal(err)
	}
	// An in-place same-size write changes mtime and must be rehashed too.
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	changedAt := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changedAt, changedAt); err != nil {
		t.Fatal(err)
	}
	if reads, err := verify(); err == nil || reads != len(data) {
		t.Fatalf("same-size edit reused stale verification: reads=%d, err=%v", reads, err)
	}
}

func TestAgentArtifactManifestCacheInvalidatesAndReturnsOwnedCopies(t *testing.T) {
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "cache-v1")
	svc := NewAgentDeploymentService(nil, dir)
	first, err := svc.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	first.BuildVersion, first.Agents[0].SHA256 = "mutated", "bad"
	again, err := svc.Manifest()
	if err != nil || again.BuildVersion != "cache-v1" || again.Agents[0].SHA256 == "bad" {
		t.Fatalf("caller mutated the cached manifest: %v %v", again, err)
	}
	agentprototest.WriteArtifacts(t, dir, "cache-v2")
	next, err := svc.Manifest()
	if err != nil || next.BuildVersion != "cache-v2" {
		t.Fatalf("manifest update retained old version: %v %v", next, err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentproto.ManifestName), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Manifest(); err == nil {
		t.Fatal("invalid changed manifest reused cached version")
	}
}
