package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/vpsmanager/backend/internal/agentproto"
)

// Bundle files are immutable in normal operation. Stat every opened file, but
// only reread its bytes when its path, identity, size, mtime or expected hash
// changes. Serialize cold verification so concurrent Range requests share it.
type agentBundleCache struct {
	mu           sync.Mutex
	manifestPath string
	manifestInfo os.FileInfo
	manifest     *agentproto.Manifest
	verified     map[string]verifiedAgentArtifact
}

type verifiedAgentArtifact struct {
	info   os.FileInfo
	digest string
}

func sameBundleFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() &&
		os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (c *agentBundleCache) loadManifest(dir string) (*agentproto.Manifest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := filepath.Join(dir, agentproto.ManifestName)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if path != c.manifestPath || !sameBundleFile(info, c.manifestInfo) {
		// Invalidate before loading, including when the changed file is invalid.
		c.manifestInfo, c.manifest = nil, nil
		c.verified = nil
		manifest, err := agentproto.LoadManifest(dir)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil || !sameBundleFile(info, after) {
			return nil, errors.New("artifact manifest changed while loading")
		}
		c.manifestPath, c.manifestInfo, c.manifest = path, after, manifest
	}
	// Callers must not be able to mutate the shared manifest or its slice.
	manifest := *c.manifest
	manifest.Agents = slices.Clone(c.manifest.Agents)
	return &manifest, nil
}

type artifactFile interface {
	io.ReadSeeker
	Stat() (os.FileInfo, error)
}

func (c *agentBundleCache) verify(f artifactFile, path string, a agentproto.Artifact) (os.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != a.Size {
		delete(c.verified, path)
		return nil, errors.New("artifact size or file type mismatch")
	}
	if cached := c.verified[path]; cached.digest == a.SHA256 && sameBundleFile(info, cached.info) {
		return info, nil
	}
	delete(c.verified, path)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !sameBundleFile(info, after) || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return nil, errors.New("artifact checksum mismatch or file changed")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if c.verified == nil {
		c.verified = make(map[string]verifiedAgentArtifact)
	}
	c.verified[path] = verifiedAgentArtifact{info: after, digest: a.SHA256}
	return after, nil
}
