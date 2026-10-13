// Package agentproto freezes the Agent artifact contract shared by the build
// script, the agent binary and the Hub's startup validation (REQUIREMENTS §8.2).
//
// The manifest and the agent's own `--format json` output carry the same build
// version and protocol version, so a Hub can refuse artifacts that do not match
// the protocol it speaks. Both linux/amd64 and linux/arm64 must be present: the
// Hub never silently degrades to a single architecture.
package agentproto

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ProtocolVersion is the wire protocol spoken by the agent's JSON output. The
// first release is 1; the Hub accepts an exact match only.
const ProtocolVersion = 1

// Version is the build identifier compiled into the agent (and the Hub) with
// -ldflags "-X .../agentproto.Version=<build>". It is never empty in a released
// artifact; "dev" means an un-stamped local build.
var Version = "dev"

// ManifestName is the manifest file inside the artifacts directory.
const ManifestName = "agents-manifest.json"

// Platforms that must both be present in every Hub image.
var RequiredPlatforms = []string{"linux/amd64", "linux/arm64"}

// Artifact is one compiled agent binary.
type Artifact struct {
	Platform        string `json:"platform"`
	File            string `json:"file"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	BuildVersion    string `json:"build_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

// Manifest describes the agent artifacts shipped with a Hub image.
type Manifest struct {
	BuildVersion    string     `json:"build_version"`
	ProtocolVersion int        `json:"protocol_version"`
	Agents          []Artifact `json:"agents"`
}

// LoadManifest reads and validates the manifest structure in dir.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agent artifacts manifest %s: %w", path, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("agent artifacts manifest %s: %w", path, err)
	}
	if manifest.BuildVersion == "" {
		return nil, errors.New("agent artifacts manifest: build_version is required")
	}
	if manifest.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("agent artifacts manifest: protocol_version %d, want %d", manifest.ProtocolVersion, ProtocolVersion)
	}
	if len(manifest.Agents) != len(RequiredPlatforms) {
		return nil, fmt.Errorf("agent artifacts manifest: %d agents, want both %s", len(manifest.Agents), strings.Join(RequiredPlatforms, " and "))
	}
	seen := map[string]bool{}
	for _, artifact := range manifest.Agents {
		if seen[artifact.Platform] {
			return nil, fmt.Errorf("agent artifacts manifest: duplicate platform %s", artifact.Platform)
		}
		seen[artifact.Platform] = true
		if err := artifact.validate(); err != nil {
			return nil, err
		}
	}
	for _, platform := range RequiredPlatforms {
		if !seen[platform] {
			return nil, fmt.Errorf("agent artifacts manifest: missing %s (no single-architecture fallback)", platform)
		}
	}
	return &manifest, nil
}

func (a Artifact) validate() error {
	if a.Platform == "" || a.File == "" {
		return errors.New("agent artifacts manifest: platform and file are required")
	}
	if filepath.Base(a.File) != a.File {
		return fmt.Errorf("agent artifacts manifest: file %q must not contain a path", a.File)
	}
	if a.Size <= 0 {
		return fmt.Errorf("agent artifacts manifest: %s size must be positive", a.Platform)
	}
	if len(a.SHA256) != sha256.Size*2 {
		return fmt.Errorf("agent artifacts manifest: %s sha256 must be a hex digest", a.Platform)
	}
	if a.BuildVersion == "" {
		return fmt.Errorf("agent artifacts manifest: %s build_version is required", a.Platform)
	}
	if a.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("agent artifacts manifest: %s protocol_version %d, want %d", a.Platform, a.ProtocolVersion, ProtocolVersion)
	}
	return nil
}

// Verify checks every artifact in dir: present, regular, exact size, exact
// SHA-256, 64-bit ELF for the declared platform, and a matching build and
// protocol version. Any mismatch is fatal: the Hub must not start.
func (m *Manifest) Verify(dir string) error {
	for _, artifact := range m.Agents {
		if artifact.BuildVersion != m.BuildVersion {
			return fmt.Errorf("agent %s: build_version %q does not match manifest %q", artifact.Platform, artifact.BuildVersion, m.BuildVersion)
		}
		path := filepath.Join(dir, artifact.File)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("agent %s: %w", artifact.Platform, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("agent %s: %s is not a regular file", artifact.Platform, path)
		}
		if info.Size() != artifact.Size {
			return fmt.Errorf("agent %s: size %d does not match manifest %d", artifact.Platform, info.Size(), artifact.Size)
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return fmt.Errorf("agent %s: %w", artifact.Platform, err)
		}
		if !strings.EqualFold(digest, artifact.SHA256) {
			return fmt.Errorf("agent %s: sha256 %s does not match manifest %s", artifact.Platform, digest, artifact.SHA256)
		}
		if err := checkELF(path, artifact.Platform); err != nil {
			return err
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
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

// checkELF rejects a binary that is not a 64-bit ELF for the declared platform,
// which is what catches a mis-stamped or cross-wired artifact.
func checkELF(path, platform string) error {
	var want elf.Machine
	switch platform {
	case "linux/amd64":
		want = elf.EM_X86_64
	case "linux/arm64":
		want = elf.EM_AARCH64
	default:
		return fmt.Errorf("agent platform %q is not supported", platform)
	}
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("agent %s: %s is not an ELF binary: %w", platform, path, err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 {
		return fmt.Errorf("agent %s: ELF class %v, want 64-bit", platform, f.Class)
	}
	if f.Machine != want {
		return fmt.Errorf("agent %s: ELF machine %v, want %v", platform, f.Machine, want)
	}
	return nil
}
