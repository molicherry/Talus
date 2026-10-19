package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

type residentJob struct {
	cancel context.CancelFunc
	hash   string
}

// Start reconciles desired installations, not sampling. A healthy resident agent
// consumes no SSH sessions. Four independent installation leases are allowed.
func (m *ResidentMonitor) Start(ctx context.Context) {
	var wg sync.WaitGroup
	defer func() {
		m.installJobsMu.Lock()
		for _, j := range m.installJobs {
			j.cancel()
		}
		m.installJobsMu.Unlock()
		wg.Wait()
		m.Close()
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var lastCleanup time.Time
	for ctx.Err() == nil {
		if m.hubURL != "" {
			query, cancel := context.WithTimeout(ctx, 10*time.Second)
			servers, err := m.servers.FindAll(query)
			cancel()
			if err == nil {
				live := make(map[uint]bool, len(servers))
				for _, s := range servers {
					live[s.ID] = true
					query, cancel = context.WithTimeout(ctx, 5*time.Second)
					n, err := m.repo.Ensure(query, s)
					cancel()
					if err != nil {
						continue
					}
					m.installJobsMu.Lock()
					if j, exists := m.installJobs[s.ID]; exists && j.hash != n.TokenHash {
						j.cancel()
					}
					m.installJobsMu.Unlock()
					m.mu.Lock()
					v := m.runtimeLocked(n)
					if v.node.TokenHash != n.TokenHash {
						if v.close != nil {
							v.close()
						}
						v = &residentRuntime{node: n}
						m.nodes[s.ID] = v
					} else {
						v.node = n
					}
					lastContact := v.lastContact
					if lastContact.IsZero() && n.LastContact != nil {
						lastContact = *n.LastContact
					}
					version := v.version
					lastSample := (*time.Time)(nil)
					if v.latest != nil {
						at := v.latest.Time
						lastSample = &at
					}
					persisted := v.lastPersisted
					m.mu.Unlock()
					if !lastContact.IsZero() {
						query, cancel = context.WithTimeout(ctx, 2*time.Second)
						_ = m.repo.Contact(query, n.ServerID, lastContact, version, lastSample, persisted)
						cancel()
					}
					manifest, err := m.deployer.Manifest()
					if err != nil {
						continue
					}
					manifestBytes, _ := json.Marshal(manifest)
					desired := repository.TokenHash(string(manifestBytes))
					config := repository.TokenHash(fmt.Sprintf("%s:%s:%d:%t", m.hubURL, n.TokenHash, m.interval, m.allowHTTP))
					if !residentInstallNeeded(n, desired, config, lastContact, m.now()) {
						continue
					}
					if n.LastAttempt != nil && time.Since(*n.LastAttempt) < time.Minute {
						continue
					}
					m.installJobsMu.Lock()
					_, busy := m.installJobs[s.ID]
					if !busy && len(m.installJobs) < 4 {
						jobCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
						m.installJobs[s.ID] = residentJob{stop, n.TokenHash}
						wg.Add(1)
						go func(n model.AgentNode) {
							defer wg.Done()
							defer stop()
							defer func() {
								m.installJobsMu.Lock()
								delete(m.installJobs, n.ServerID)
								m.installJobsMu.Unlock()
								m.NotifyTopologyChange()
							}()
							m.installJob(jobCtx, n, desired, config)
						}(n)
					}
					m.installJobsMu.Unlock()
				}
				m.mu.Lock()
				for id, v := range m.nodes {
					if !live[id] {
						if v.close != nil {
							v.close()
						}
						delete(m.nodes, id)
					}
				}
				m.mu.Unlock()
				m.installJobsMu.Lock()
				for id, j := range m.installJobs {
					if !live[id] {
						j.cancel()
					}
				}
				m.installJobsMu.Unlock()
			}
			if time.Since(lastCleanup) > time.Hour {
				query, cancel := context.WithTimeout(ctx, 5*time.Second)
				_ = m.repo.Cleanup(query)
				cancel()
				lastCleanup = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

func (m *ResidentMonitor) installJob(ctx context.Context, n model.AgentNode, desired, configHash string) {
	state, reason := "installing", ""
	_ = m.repo.Deployment(ctx, n, state, reason, n.InstalledHash, n.ConfigHash)
	m.setDeployment(n, state, reason)
	err := m.install(ctx, n)
	state = "ready"
	if err != nil {
		state = "failed"
		reason = deployFailureReason(err)
		// Record what is actually running after a rollback, not what the failed
		// attempt wanted. The next reconciliation retries anyway because a
		// non-ready deployment is never skipped.
		desired = n.InstalledHash
		configHash = n.ConfigHash
		slog.Warn("resident agent installation failed", "server_id", n.ServerID, "reason", reason)
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = m.repo.Deployment(storeCtx, n, state, reason, desired, configHash)
	m.setDeployment(n, state, reason)
}

func (m *ResidentMonitor) setDeployment(n model.AgentNode, state, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.runtimeLocked(n)
	if v.node.TokenHash == n.TokenHash {
		v.node.Deployment = state
		v.node.DeployError = reason
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func unitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`).Replace(s) + `"`
}

func (m *ResidentMonitor) install(ctx context.Context, n model.AgentNode) error {
	lease, err := m.deployer.ssh.AcquireLease(ctx, n.ServerID, LeaseConsumerAgent)
	if err != nil {
		return err
	}
	defer lease.Release()
	platform, err := m.deployer.Probe(ctx, lease)
	if err != nil {
		return err
	}
	artifact, err := m.deployer.artifactFor(platform)
	if err != nil {
		return err
	}
	result, err := lease.Exec(ctx, `id -u; printf '%s\n' "$HOME"; command -v systemctl`, 5*time.Second)
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return deployError("service_manager_unavailable", errors.New("systemd is required for resident agent installation"))
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "/") || len(lines[1]) > 512 || strings.ContainsAny(lines[1], "\r\n\x00") {
		return deployError("service_manager_unavailable", errors.New("cannot resolve installation paths"))
	}
	base := "/var/lib/talus-agent/" + n.InstallID
	unitDir := "/etc/systemd/system"
	ctl := "systemctl"
	if lines[0] != "0" {
		base = lines[1] + "/.local/share/talus-agent/" + n.InstallID
		unitDir = lines[1] + "/.config/systemd/user"
		ctl = "systemctl --user"
	}
	check := ctl + " show-environment >/dev/null 2>&1"
	if lines[0] != "0" {
		check += " && test -e /var/lib/systemd/linger/$(id -un)"
	}
	result, err = lease.Exec(ctx, check, 5*time.Second)
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return deployError("service_manager_unavailable", errors.New("systemd service permissions and user lingering are required"))
	}
	setup := "test ! -L " + shellQuote(base) + " && mkdir -p " + shellQuote(base) + " " + shellQuote(unitDir) + " && chmod 0700 " + shellQuote(base) + " && rm -f " + shellQuote(base) + "/.next-*"
	if result, err = lease.Exec(ctx, setup, 5*time.Second); err != nil {
		return err
	} else if result == nil || result.ExitCode != 0 {
		return deployError(ReasonAgentReadOnly, errors.New("persistent installation directory unavailable"))
	}
	token, err := m.repo.Secret(n)
	if err != nil {
		return err
	}
	cfg := agentwire.Config{HubURL: m.hubURL, Token: token, StateDir: base + "/outbox", IntervalSeconds: m.interval, AllowHTTP: m.allowHTTP}
	if err = cfg.Validate(); err != nil {
		return err
	}
	unitName := "talus-agent-" + n.InstallID + ".service"
	binary := base + "/agent"
	config := base + "/config.json"
	unit := unitDir + "/" + unitName
	unitText := "[Unit]\nDescription=Talus monitoring agent\nAfter=network-online.target\nStartLimitIntervalSec=0\n[Service]\nType=simple\nExecStart=" + unitQuote(binary) + " --daemon --config " + unitQuote(config) + "\nRestart=always\nRestartSec=3\nKillMode=control-group\nTimeoutStopSec=10\nUMask=0077\nNoNewPrivileges=true\n[Install]\nWantedBy="
	if lines[0] == "0" {
		unitText += "multi-user.target\n"
	} else {
		unitText += "default.target\n"
	}
	id, err := agentwire.NewID()
	if err != nil {
		return err
	}
	next := base + "/.next-" + id
	if err = lease.Upload(ctx, filepath.Join(m.deployer.artifactsDir, artifact.File), next, 2*time.Minute); err != nil {
		return err
	}
	hash, err := m.deployer.remoteHash(ctx, lease, shellQuote(next))
	if err != nil {
		return err
	}
	if hash != artifact.SHA256 {
		return deployError(ReasonAgentHashMismatch, errors.New("resident artifact verification failed"))
	}
	configBytes, _ := json.Marshal(cfg)
	for _, f := range []struct {
		suffix string
		data   []byte
	}{{".config", configBytes}, {".unit", []byte(unitText)}} {
		local, err := os.CreateTemp("", "talus-agent-config-")
		if err != nil {
			return err
		}
		localPath := local.Name()
		_, err = local.Write(f.data)
		if closeErr := local.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = lease.Upload(ctx, localPath, next+f.suffix, 30*time.Second)
		}
		_ = os.Remove(localPath)
		if err != nil {
			return err
		}
		hash, err = m.deployer.remoteHash(ctx, lease, shellQuote(next+f.suffix))
		if err != nil {
			return err
		}
		if hash != repository.TokenHash(string(f.data)) {
			return deployError(ReasonAgentHashMismatch, errors.New("resident configuration verification failed"))
		}
	}
	// Keep the prior executable/configuration until the new service demonstrates
	// authenticated contact with the Hub. Roll back locally even if SSH drops.
	script := residentActivationScript(binary, config, unit, unitName, next, ctl, 20)
	result, err = lease.Exec(ctx, script, 45*time.Second)
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return deployError(ReasonAgentHealthCheckFailed, errors.New("new service did not authenticate; previous version restored when available"))
	}
	return lease.releaseAndWait()
}

func residentActivationScript(binary, config, unit, unitName, next, ctl string, attempts int) string {
	q := shellQuote
	return "set -eu\nhad_previous=0\nactivated=0\n" +
		"finish() { rc=$?; set +e; trap - EXIT; if test \"$rc\" != 0 && test \"$activated\" = 1; then " + ctl + " stop " + q(unitName) + " || true; if test \"$had_previous\" = 1; then mv -f " + q(binary+".previous") + " " + q(binary) + "; mv -f " + q(config+".previous") + " " + q(config) + "; if test -f " + q(unit+".previous") + "; then mv -f " + q(unit+".previous") + " " + q(unit) + "; fi; " + ctl + " daemon-reload || true; " + ctl + " restart " + q(unitName) + " || true; fi; fi; rm -f " + q(next) + " " + q(next+".config") + " " + q(next+".unit") + "; exit \"$rc\"; }; trap finish EXIT; trap 'exit 1' HUP INT TERM\n" +
		"if test -f " + q(binary) + " && test -f " + q(config) + "; then cp -p " + q(binary) + " " + q(binary+".previous") + "; cp -p " + q(config) + " " + q(config+".previous") + "; if test -f " + q(unit) + "; then cp -p " + q(unit) + " " + q(unit+".previous") + "; fi; had_previous=1; fi\n" +
		"chmod 0700 " + q(next) + "\nactivated=1\nmv -f " + q(next) + " " + q(binary) + "\nmv -f " + q(next+".config") + " " + q(config) + "\nmv -f " + q(next+".unit") + " " + q(unit) + "\nrm -f " + q(filepath.Join(filepath.Dir(binary), "outbox", "health.json")) + "\n" +
		ctl + " daemon-reload\n" + ctl + " enable " + q(unitName) + "\n" + ctl + " restart " + q(unitName) + "\n" +
		fmt.Sprintf("i=0\nwhile test \"$i\" -lt %d; do if %s --health --config %s >/dev/null 2>&1; then exit 0; fi; i=$((i + 1)); sleep 1; done\nexit 1", attempts, q(binary), q(config))
}

// residentInstallNeeded reports whether the reconciler must (re)install the
// agent. Only a ready deployment with matching fingerprints and a recent
// contact is skipped, so a failed or interrupted attempt is retried instead of
// staying frozen in the failed state.
func residentInstallNeeded(n model.AgentNode, desired, config string, lastContact, now time.Time) bool {
	if n.Deployment == "ready" && n.InstalledHash == desired && n.ConfigHash == config && now.Sub(lastContact) < 90*time.Second {
		return false
	}
	return true
}

// deployFailureReason maps an installation error onto the stable reason class
// the UI renders, so SSH failures stay distinguishable from install failures.
func deployFailureReason(err error) string {
	if reason := ReasonFromError(err); reason != "" {
		return reason
	}
	var app *server.AppError
	if errors.As(err, &app) && app.Reason != "" {
		return app.Reason
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "deployment_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deployment_timeout"
	}
	return "deployment_failed"
}

// RemoveAgent uninstalls the resident agent from a server that is about to be
// deleted and drops its credential record. Removal is best effort -- the host
// may already be gone -- but the record is always dropped so a revoked
// credential cannot be resurrected by a later topology change.
func (m *ResidentMonitor) RemoveAgent(ctx context.Context, id uint) error {
	n, err := m.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	m.cancelInstall(id)
	var removeErr error
	if m.deployer != nil && m.hubURL != "" {
		removeErr = m.uninstall(ctx, n)
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.repo.Delete(storeCtx, id); err != nil && removeErr == nil {
		removeErr = err
	}
	return removeErr
}

func (m *ResidentMonitor) cancelInstall(id uint) {
	m.installJobsMu.Lock()
	job, ok := m.installJobs[id]
	m.installJobsMu.Unlock()
	if ok {
		job.cancel()
	}
}

// uninstall stops the unit and removes the installation directory. The layout
// follows the account the installer used: root installs under /var/lib and
// /etc/systemd/system, unprivileged installs under the account's home.
func (m *ResidentMonitor) uninstall(ctx context.Context, n model.AgentNode) error {
	lease, err := m.deployer.ssh.AcquireLease(ctx, n.ServerID, LeaseConsumerAgent)
	if err != nil {
		return err
	}
	defer lease.Release()
	base := "/var/lib/talus-agent/" + n.InstallID
	unitDir := "/etc/systemd/system"
	ctl := "systemctl"
	if result, probeErr := lease.Exec(ctx, `id -u; printf '%s\n' "$HOME"`, 5*time.Second); probeErr == nil && result != nil && result.ExitCode == 0 {
		lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
		if len(lines) == 2 && lines[0] != "0" && strings.HasPrefix(lines[1], "/") {
			base = lines[1] + "/.local/share/talus-agent/" + n.InstallID
			unitDir = lines[1] + "/.config/systemd/user"
			ctl = "systemctl --user"
		}
	}
	unitName := "talus-agent-" + n.InstallID + ".service"
	result, err := lease.Exec(ctx, residentUninstallScript(base, unitDir+"/"+unitName, unitName, ctl), 30*time.Second)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("agent uninstall did not run")
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("agent uninstall exited %d", result.ExitCode)
	}
	return nil
}

// residentUninstallScript stops and deletes one installation layout. A missing
// unit or directory is not an error, so it is safe to run before deleting the
// server record.
func residentUninstallScript(base, unit, unitName, ctl string) string {
	q := shellQuote
	return "set -u\n" +
		ctl + " disable --now " + q(unitName) + " >/dev/null 2>&1 || true\n" +
		"rm -f " + q(unit) + "\n" +
		ctl + " daemon-reload >/dev/null 2>&1 || true\n" +
		"rm -rf " + q(base) + "\n"
}
