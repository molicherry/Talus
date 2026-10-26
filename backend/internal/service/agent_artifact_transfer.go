package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/agentwindows"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/repository"
)

//go:embed scripts/agent_fetch.sh
var linuxArtifactFetch string

type artifactDownload struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

func artifactCurlConfig(download artifactDownload, digest string) []byte {
	quote := func(s string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) }
	protocols := "=https"
	if download.AllowHTTP {
		protocols = "=http,https"
	}
	return []byte("url = \"" + quote(download.URL) + "\"\nheader = \"Authorization: Bearer " + download.Token + "\"\nheader = \"If-Range: \\\"" + digest + "\\\"\"\nheader = \"Accept-Encoding: identity\"\nproto = \"" + protocols + "\"\n")
}

func uploadResidentBytes(ctx context.Context, lease *sshLease, data []byte, remote string, windows bool) error {
	f, err := os.CreateTemp("", "talus-bootstrap-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if windows {
		return lease.uploadPrivateDirectory(ctx, f.Name(), "/"+remote, 30*time.Second)
	}
	return lease.Upload(ctx, f.Name(), remote, 30*time.Second)
}

func linuxArtifactCommand(base, current, next, config, mode string, artifact agentproto.Artifact, seconds int) string {
	q := shellQuote
	return "set -eu\ncache_dir=" + q(base+"/cache") + "\ncurrent=" + q(current) + "\nnext=" + q(next) + "\nfetch_config=" + q(config) + "\nmode=" + q(mode) + "\ndigest=" + q(artifact.SHA256) + "\nsize=" + strconv.FormatInt(artifact.Size, 10) + "\nseconds=" + strconv.Itoa(seconds) + "\n" + linuxArtifactFetch
}

// stageArtifact first reuses verified cached/installed bytes, then attempts an
// authenticated HTTP pull with digest-bound resumption. Only an unavailable
// pull falls back to SFTP. A progressing interrupted pull keeps its partial
// bytes for the next attempt instead of restarting a second full transfer.
func (m *ResidentMonitor) stageArtifact(ctx context.Context, lease *sshLease, artifact agentproto.Artifact, cfg agentwire.Config, base, current, next string, layout *agentwindows.Layout) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, agentArtifactUploadBudget)
	defer cancel()
	download := artifactDownload{URL: strings.TrimRight(cfg.HubURL, "/") + "/api/v1/agent/artifacts/" + artifact.SHA256, Token: cfg.Token, AllowHTTP: cfg.AllowHTTP}
	configPath := next + ".fetch"
	windows := layout != nil
	var data []byte
	if windows {
		data, _ = json.Marshal(download)
	} else {
		data = artifactCurlConfig(download, artifact.SHA256)
	}
	if err := uploadResidentBytes(ctx, lease, data, configPath, windows); err != nil {
		return err
	}
	run := func(mode string) (*ExecResult, error) {
		seconds := 1
		if deadline, ok := ctx.Deadline(); ok {
			seconds = max(1, int(time.Until(deadline).Seconds()))
		}
		if windows {
			scriptPath := next + ".fetch.ps1"
			script := layout.FetchFile(configPath, artifact.SHA256, artifact.Size, mode, seconds)
			if err := uploadResidentBytes(ctx, lease, script, scriptPath, true); err != nil {
				return nil, err
			}
			deadline, _ := ctx.Deadline()
			return lease.execTransfer(ctx, agentwindows.Command(agentwindows.RunFetch(scriptPath, repository.TokenHash(string(script)))), max(0, time.Until(deadline)))
		}
		deadline, _ := ctx.Deadline()
		return lease.execTransfer(ctx, linuxArtifactCommand(base, current, next, configPath, mode, artifact, seconds), max(0, time.Until(deadline)))
	}
	remaining := func() time.Duration { deadline, _ := ctx.Deadline(); return max(0, time.Until(deadline)) }
	r, err := run("pull")
	if err != nil {
		return err
	}
	if r != nil && r.ExitCode == 0 && strings.TrimSpace(r.Stdout) == "TALUS_ARTIFACT_READY" {
		return nil
	}
	if r != nil && r.ExitCode == 77 {
		return deployError(ReasonAgentHashMismatch, errors.New("downloaded artifact checksum mismatch"))
	}
	if r != nil && r.ExitCode == 76 {
		return errors.New("artifact pull interrupted; partial download retained")
	}
	if r == nil || r.ExitCode != 75 || strings.TrimSpace(r.Stdout) != "TALUS_PULL_UNAVAILABLE" {
		return errors.New("artifact cache preparation failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	local := filepath.Join(m.deployer.artifactsDir, artifact.File)
	if windows {
		err = lease.uploadPrivateDirectory(ctx, local, "/"+next, remaining())
	} else {
		err = lease.Upload(ctx, local, next, remaining())
	}
	if err != nil {
		return err
	}
	// Publishing the fallback also verifies bytes and seeds the digest cache.
	r, err = run("store")
	if err != nil {
		return err
	}
	if r == nil || r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "TALUS_ARTIFACT_READY" {
		return deployError(ReasonAgentHashMismatch, errors.New("fallback artifact verification failed"))
	}
	return nil
}
