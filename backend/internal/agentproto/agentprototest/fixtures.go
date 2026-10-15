// Package agentprototest builds tiny real ELF fixtures for the agent artifact
// contract, so tests exercise the same size/hash/architecture checks the Hub
// performs at startup without compiling the full agent twice.
package agentprototest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vpsmanager/backend/internal/agentproto"
)

// WriteArtifacts cross-compiles a minimal ELF for linux/amd64 and linux/arm64
// into dir and writes a matching manifest for version.
func WriteArtifacts(tb testing.TB, dir, version string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("create artifacts dir: %v", err)
	}
	source := filepath.Join(tb.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		tb.Fatalf("write fixture source: %v", err)
	}

	agents := make([]agentproto.Artifact, 0, 2)
	for _, target := range []struct{ platform, arch string }{
		{"linux/amd64", "amd64"},
		{"linux/arm64", "arm64"},
	} {
		out := filepath.Join(dir, "vpsmanager-agent-linux-"+target.arch)
		cmd := exec.Command("go", "build", "-trimpath", "-o", out, source)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+target.arch)
		if combined, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("build %s fixture: %v\n%s", target.arch, err, combined)
		}
		info, err := os.Stat(out)
		if err != nil {
			tb.Fatalf("stat %s fixture: %v", target.arch, err)
		}
		digest, err := digestOf(out)
		if err != nil {
			tb.Fatalf("digest %s fixture: %v", target.arch, err)
		}
		agents = append(agents, agentproto.Artifact{
			Platform:        target.platform,
			File:            filepath.Base(out),
			SHA256:          digest,
			Size:            info.Size(),
			BuildVersion:    version,
			ProtocolVersion: agentproto.ProtocolVersion,
		})
	}

	manifest := agentproto.Manifest{
		BuildVersion:    version,
		ProtocolVersion: agentproto.ProtocolVersion,
		Agents:          agents,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		tb.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentproto.ManifestName), append(raw, '\n'), 0o644); err != nil {
		tb.Fatalf("write manifest: %v", err)
	}
}

func digestOf(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
