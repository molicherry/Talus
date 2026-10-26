package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/agentwindows"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

const reasonWindowsService = "agent_windows_service_unavailable"

func windowsAgentLayout(installID, home string) agentLayout {
	l := agentwindows.Layout{Home: home, InstallID: installID}
	return agentLayout{OS: "windows", Home: home, Base: l.Base()}
}

func discoverWindowsLayout(ctx context.Context, installID string, lease Lease) (agentLayout, error) {
	if !validDiscoveryInstallID(installID) {
		return agentLayout{}, errors.New("invalid installation id")
	}
	result, err := lease.Exec(ctx, agentwindows.Command(agentwindows.Context), 10*time.Second)
	if err != nil {
		return agentLayout{}, err
	}
	if result == nil || result.ExitCode != 0 || result.OutputTruncated || result.StdoutTruncated {
		return agentLayout{}, deployError(reasonWindowsService, errors.New("windows deployment context unavailable"))
	}
	var v struct {
		Home  string
		Admin bool
	}
	if err = json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &v); err != nil {
		return agentLayout{}, deployError(reasonWindowsService, errors.New("invalid Windows deployment context"))
	}
	home := strings.ReplaceAll(v.Home, "\\", "/")
	if !v.Admin || !agentwindows.ValidHome(home) {
		return agentLayout{}, deployError(reasonWindowsService, errors.New("windows requires an administrator and a local ProgramData directory"))
	}
	return windowsAgentLayout(installID, home), nil
}

func windowsExec(ctx context.Context, lease Lease, script, reason string, budget time.Duration) error {
	r, err := lease.Exec(ctx, agentwindows.Command(script), budget)
	if err != nil {
		return err
	}
	if r == nil || r.ExitCode != 0 || r.OutputTruncated || r.StdoutTruncated {
		return deployError(reason, errors.New("windows agent deployment step failed"))
	}
	return nil
}

func verifyWindowsFile(ctx context.Context, lease Lease, name, expected string) error {
	r, err := lease.Exec(ctx, agentwindows.Command(agentwindows.Hash(name)), 10*time.Second)
	if err != nil {
		return err
	}
	if r == nil || r.ExitCode != 0 || r.OutputTruncated || r.StdoutTruncated || !strings.EqualFold(strings.TrimSpace(r.Stdout), expected) {
		return deployError(ReasonAgentHashMismatch, errors.New("windows artifact verification failed"))
	}
	return nil
}

func (m *ResidentMonitor) installWindows(ctx context.Context, n model.AgentNode, lease *sshLease, artifact agentproto.Artifact) error {
	layout, err := discoverWindowsLayout(ctx, n.InstallID, lease)
	if err != nil {
		return err
	}
	for _, old := range knownInstallLayouts(n) {
		if old.OS != "windows" || !strings.EqualFold(old.Base, layout.Base) {
			return deployError(reasonWindowsService, errors.New("previous installation is in a different platform or ProgramData directory"))
		}
	}
	// Persist before any remote writes, including failed first installations.
	target := model.AgentInstallLayout{OS: "windows", Home: layout.Home}
	if err = m.repo.TrackInstallLayouts(ctx, n, []model.AgentInstallLayout{target}); err != nil {
		return err
	}
	attempt, err := agentwire.NewID()
	if err != nil {
		return err
	}
	l := agentwindows.Layout{Home: layout.Home, InstallID: n.InstallID, Attempt: attempt}
	if err = windowsExec(ctx, lease, l.Prepare(), reasonWindowsService, 20*time.Second); err != nil {
		return err
	}
	token, err := m.repo.Secret(n)
	if err != nil {
		return err
	}
	settings := m.installSettings(ctx)
	cfg := agentwire.Config{HubURL: settings.URL, Token: token, StateDir: l.Base() + "/outbox", IntervalSeconds: settings.Interval, AllowHTTP: settings.AllowHTTP}
	if err = cfg.Validate(); err != nil {
		return err
	}
	config, _ := json.Marshal(cfg)
	configHash := repository.TokenHash(string(config))
	activation := l.ActivationFile(artifact.SHA256, configHash)
	// Windows OpenSSH does not offer POSIX chmod. Prepare has already restricted
	// the staging directory to Administrators and SYSTEM, before secrets arrive.
	if err = lease.uploadPrivateDirectory(ctx, filepath.Join(m.deployer.artifactsDir, artifact.File), "/"+l.Stage()+"/agent.exe", residentArtifactUploadBudget); err != nil {
		return err
	}
	if err = verifyWindowsFile(ctx, lease, l.Stage()+"/agent.exe", artifact.SHA256); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"config.json", config}, {"activate.ps1", activation}} {
		local, err := os.CreateTemp("", "talus-windows-")
		if err != nil {
			return err
		}
		_, err = local.Write(f.data)
		if closeErr := local.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = lease.uploadPrivateDirectory(ctx, local.Name(), "/"+path.Join(l.Stage(), f.name), 30*time.Second)
		}
		_ = os.Remove(local.Name())
		if err != nil {
			return err
		}
		if err = verifyWindowsFile(ctx, lease, path.Join(l.Stage(), f.name), repository.TokenHash(string(f.data))); err != nil {
			return err
		}
	}
	// Task Scheduler detaches activation from the SSH channel. A lost connection
	// cannot interrupt the health check/rollback or leave a half-installed service.
	if err = windowsExec(ctx, lease, l.Launch(repository.TokenHash(string(activation))), reasonWindowsService, 15*time.Second); err != nil {
		return err
	}
	if err = windowsExec(ctx, lease, l.Wait(agentwindows.ActivationWaitSeconds), ReasonAgentHealthCheckFailed, (agentwindows.ActivationWaitSeconds+10)*time.Second); err != nil {
		return err
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	err = m.repo.SetInstallPlatformLayout(storeCtx, n, target)
	cancel()
	if err != nil {
		return err
	}
	return lease.releaseAndWait()
}

func (m *ResidentMonitor) uninstallWindows(ctx context.Context, installID string, layout agentLayout, lease *sshLease) error {
	if !validDiscoveryInstallID(installID) || !agentwindows.ValidHome(layout.Home) {
		return errors.New("invalid Windows installation layout")
	}
	l := agentwindows.Layout{Home: layout.Home, InstallID: installID}
	status := "not_scheduled"
	defer func() {
		if status != "succeeded" {
			level := slog.LevelWarn
			if status == "pending" {
				level = slog.LevelInfo
			}
			slog.Log(ctx, level, "Windows agent removal not confirmed; inspect local task and receipt", "install_id", installID, "status", status, "task", l.Name()+"-Remove", "receipt", l.Root()+"/"+l.Name()+".remove.json")
		}
	}()
	if err := windowsExec(ctx, lease, l.PrepareRemoval(), reasonWindowsService, 5*time.Second); err != nil {
		return err
	}
	local, err := os.CreateTemp("", "talus-windows-remove-")
	if err != nil {
		return err
	}
	defer os.Remove(local.Name())
	_, err = local.Write(l.RemovalFile())
	if closeErr := local.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = lease.uploadPrivateDirectory(ctx, local.Name(), "/"+l.RemovalPath(), 5*time.Second); err != nil {
		return err
	}
	if err := windowsExec(ctx, lease, l.RemoveDetached(), reasonWindowsService, 5*time.Second); err != nil {
		return err
	}
	status = "receipt_unavailable"
	r, err := lease.Exec(ctx, agentwindows.Command(l.WaitRemoval(2)), 4*time.Second)
	if err != nil {
		return err
	}
	if r == nil || r.ExitCode != 0 || r.OutputTruncated || r.StdoutTruncated {
		return deployError(reasonWindowsService, errors.New("windows removal receipt unavailable; inspect the retained removal task"))
	}
	switch strings.TrimSpace(r.Stdout) {
	case "TALUS_REMOVE_OK":
		status = "succeeded"
		return nil
	case "TALUS_REMOVE_PENDING":
		status = "pending"
		return nil
	case "TALUS_REMOVE_FAILED":
		status = "failed"
		return deployError(reasonWindowsService, errors.New("windows removal failed; inspect the retained removal task and receipt"))
	default:
		return deployError(reasonWindowsService, errors.New("invalid Windows removal receipt"))
	}
}
