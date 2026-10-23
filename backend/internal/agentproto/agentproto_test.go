package agentproto_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
)

func writeManifest(t *testing.T, dir string, manifest agentproto.Manifest) {
	t.Helper()
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentproto.ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadManifest(t *testing.T, dir string) *agentproto.Manifest {
	t.Helper()
	manifest, err := agentproto.LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return manifest
}

// TestVerifyAcceptsMatchingArtifacts pins the happy path used by a correct Hub
// image: two architectures, exact size/hash/arch and a matching protocol.
func TestVerifyAcceptsMatchingArtifacts(t *testing.T) {
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "v1.2.3-test.1")

	manifest := loadManifest(t, dir)
	if err := manifest.Verify(dir); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if manifest.ProtocolVersion != agentproto.ProtocolVersion {
		t.Fatalf("protocol = %d, want %d", manifest.ProtocolVersion, agentproto.ProtocolVersion)
	}
}

func TestWindowsArtifactMustBeAMD64PE(t *testing.T) {
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "windows-test")
	m := loadManifest(t, dir)
	var windowsIndex int
	for i, a := range m.Agents {
		if a.Platform == "windows/amd64" {
			windowsIndex = i
		}
	}
	// A correctly hashed Linux executable must still be refused for Windows.
	b, err := os.ReadFile(filepath.Join(dir, m.Agents[0].File))
	if err != nil {
		t.Fatal(err)
	}
	a := &m.Agents[windowsIndex]
	if err = os.WriteFile(filepath.Join(dir, a.File), b, 0600); err != nil {
		t.Fatal(err)
	}
	a.Size = int64(len(b))
	h := sha256.Sum256(b)
	a.SHA256 = hex.EncodeToString(h[:])
	if err = m.Verify(dir); err == nil {
		t.Fatal("Linux executable accepted for Windows")
	}
	// Omitting the Windows artifact cannot silently ship a Linux-only image.
	m.Agents = append(m.Agents[:windowsIndex], m.Agents[windowsIndex+1:]...)
	writeManifest(t, dir, *m)
	if _, err = agentproto.LoadManifest(dir); err == nil {
		t.Fatal("missing Windows artifact accepted")
	}
}

// TestVerifyRejectsTamperedArtifacts pins TC-08-08: a missing, resized, modified
// or cross-wired artifact must fail startup validation.
func TestVerifyRejectsTamperedArtifacts(t *testing.T) {
	base := func(t *testing.T) (string, agentproto.Manifest) {
		dir := t.TempDir()
		agentprototest.WriteArtifacts(t, dir, "v1.2.3-test.1")
		return dir, *loadManifest(t, dir)
	}

	t.Run("missing artifact", func(t *testing.T) {
		dir, manifest := base(t)
		if err := os.Remove(filepath.Join(dir, manifest.Agents[1].File)); err != nil {
			t.Fatal(err)
		}
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("a missing agent binary must fail verification")
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		dir, manifest := base(t)
		manifest.Agents[0].Size++
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("a size mismatch must fail verification")
		}
	})

	t.Run("hash mismatch", func(t *testing.T) {
		dir, manifest := base(t)
		manifest.Agents[0].SHA256 = strings.Repeat("0", 64)
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("a hash mismatch must fail verification")
		}
	})

	t.Run("cross-wired architectures", func(t *testing.T) {
		dir, manifest := base(t)
		// Replace the arm64 binary with the amd64 one and keep the manifest
		// consistent, so only the ELF machine check can catch it.
		amd64, err := os.ReadFile(filepath.Join(dir, manifest.Agents[0].File))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, manifest.Agents[1].File)
		if err := os.WriteFile(target, amd64, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest.Agents[1].Size = int64(len(amd64))
		manifest.Agents[1].SHA256 = digestOf(t, target)
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("an amd64 binary published as arm64 must fail verification")
		}
	})

	t.Run("build version mismatch", func(t *testing.T) {
		dir, manifest := base(t)
		manifest.Agents[0].BuildVersion = "v9.9.9"
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("a per-artifact build version mismatch must fail verification")
		}
	})

	t.Run("not an elf", func(t *testing.T) {
		dir, manifest := base(t)
		target := filepath.Join(dir, manifest.Agents[0].File)
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		manifest.Agents[0].Size = int64(len("#!/bin/sh\necho hi\n"))
		manifest.Agents[0].SHA256 = digestOf(t, target)
		if err := manifest.Verify(dir); err == nil {
			t.Fatal("a non-ELF artifact must fail verification")
		}
	})
}

// TestLoadManifestRejectsIncompleteContracts pins that a single-architecture or
// mis-declared manifest is refused instead of degrading the deployment.
func TestLoadManifestRejectsIncompleteContracts(t *testing.T) {
	valid := func(t *testing.T) (string, agentproto.Manifest) {
		dir := t.TempDir()
		agentprototest.WriteArtifacts(t, dir, "v1.2.3-test.1")
		return dir, *loadManifest(t, dir)
	}

	t.Run("single architecture is refused", func(t *testing.T) {
		dir, manifest := valid(t)
		manifest.Agents = manifest.Agents[:1]
		writeManifest(t, dir, manifest)
		if _, err := agentproto.LoadManifest(dir); err == nil {
			t.Fatal("a single-architecture manifest must be refused")
		}
	})

	t.Run("protocol mismatch is refused", func(t *testing.T) {
		dir, manifest := valid(t)
		manifest.ProtocolVersion = agentproto.ProtocolVersion + 1
		writeManifest(t, dir, manifest)
		if _, err := agentproto.LoadManifest(dir); err == nil {
			t.Fatal("a future protocol version must be refused")
		}
	})

	t.Run("artifact protocol mismatch is refused", func(t *testing.T) {
		dir, manifest := valid(t)
		manifest.Agents[0].ProtocolVersion = 0
		writeManifest(t, dir, manifest)
		if _, err := agentproto.LoadManifest(dir); err == nil {
			t.Fatal("an artifact with a different protocol version must be refused")
		}
	})

	t.Run("duplicate platform is refused", func(t *testing.T) {
		dir, manifest := valid(t)
		manifest.Agents[1].Platform = "linux/amd64"
		writeManifest(t, dir, manifest)
		if _, err := agentproto.LoadManifest(dir); err == nil {
			t.Fatal("a duplicated platform must be refused")
		}
	})

	t.Run("path traversal in file is refused", func(t *testing.T) {
		dir, manifest := valid(t)
		manifest.Agents[0].File = "../vpsmanager-agent-linux-amd64"
		writeManifest(t, dir, manifest)
		if _, err := agentproto.LoadManifest(dir); err == nil {
			t.Fatal("a file name containing a path must be refused")
		}
	})

	t.Run("missing manifest is refused", func(t *testing.T) {
		if _, err := agentproto.LoadManifest(t.TempDir()); err == nil {
			t.Fatal("a missing manifest must be refused")
		}
	})
}

func digestOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
