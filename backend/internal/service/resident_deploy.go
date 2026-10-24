package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/agentwindows"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

type residentJob struct {
	cancel context.CancelFunc
	hash   string
	done   chan struct{}
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
	lastSettings := m.settings()
	for ctx.Err() == nil {
		desiredSettings := m.settings()
		if desiredSettings != lastSettings {
			// Finish cancellation/rollback before replacing a job for that node.
			// Each existing job keeps the snapshot with which it was scheduled.
			m.installJobsMu.Lock()
			m.installEpoch++
			for _, j := range m.installJobs {
				j.cancel()
			}
			m.installJobsMu.Unlock()
			lastSettings = desiredSettings
		}
		if desiredSettings.URL != "" {
			m.installJobsMu.Lock()
			epoch := m.installEpoch
			m.installJobsMu.Unlock()
			query, cancel := context.WithTimeout(ctx, 10*time.Second)
			servers, err := m.servers.FindAll(query)
			cancel()
			if err == nil {
				live := make(map[uint]bool, len(servers))
				for _, s := range servers {
					live[s.ID] = true
					if !m.installAllowed(s.ID, epoch) {
						continue
					}
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
					config := desiredSettings.hash(n.TokenHash)
					if !residentInstallNeeded(n, desired, config, lastContact, m.now()) {
						continue
					}
					if n.LastAttempt != nil && time.Since(*n.LastAttempt) < time.Minute {
						continue
					}
					m.installJobsMu.Lock()
					_, busy := m.installJobs[s.ID]
					if !busy && m.removing[s.ID] == nil && epoch == m.installEpoch && len(m.installJobs) < 4 {
						// Include upload time before the detached Windows activation
						// task's three-minute limit and its completion receipt.
						budget := 3 * time.Minute
						if n.InstallOS != "linux" {
							budget = 10 * time.Minute
						}
						jobCtx, stop := context.WithTimeout(ctx, budget)
						jobCtx = context.WithValue(jobCtx, residentSettingsKey{}, desiredSettings)
						done := make(chan struct{})
						m.installJobs[s.ID] = residentJob{cancel: stop, hash: n.TokenHash, done: done}
						wg.Add(1)
						go func(n model.AgentNode) {
							defer wg.Done()
							defer stop()
							defer func() {
								m.installJobsMu.Lock()
								delete(m.installJobs, n.ServerID)
								close(done)
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
	if platform.OS == "windows" {
		return m.installWindows(ctx, n, lease, artifact)
	}
	result, err := lease.Exec(ctx, `id -u; printf '%s\n' "$HOME"; command -v systemctl; command -v flock >/dev/null`, 5*time.Second)
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return deployError("service_manager_unavailable", errors.New("systemd and flock are required for resident agent installation"))
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 {
		return deployError("service_manager_unavailable", errors.New("cannot resolve installation paths"))
	}
	uid, convErr := strconv.Atoi(lines[0])
	if convErr != nil || uid < 0 || !validInstallHome(lines[1]) {
		return deployError("service_manager_unavailable", errors.New("cannot resolve installation paths"))
	}
	layout := agentLayoutFor(n.InstallID, uid, lines[1])
	base, unitDir, ctl := layout.Base, layout.UnitDir, layout.Control()
	check := ctl + " show-environment >/dev/null 2>&1"
	if uid != 0 {
		check += " && test -e /var/lib/systemd/linger/$(id -un)"
	}
	result, err = lease.Exec(ctx, check, 5*time.Second)
	if err != nil {
		return err
	}
	if result == nil || result.ExitCode != 0 {
		return deployError("service_manager_unavailable", errors.New("systemd service permissions and user lingering are required"))
	}
	previous := knownInstallLayouts(n)
	if len(previous) == 0 {
		previous, err = discoverInstallLayouts(ctx, n, lease)
		if err != nil {
			return err
		}
	}
	retirements, err := residentInstallRetirements(ctx, lease, layout, previous)
	if err != nil {
		return err
	}
	// Persist every location before touching the remote filesystem. A failed
	// activation, retirement, or final database write must leave enough evidence
	// for both a retry and deletion to clean all installations.
	pending := make([]model.AgentInstallLayout, 0, len(previous)+1)
	for _, old := range previous {
		pending = append(pending, model.AgentInstallLayout{OS: old.OS, UID: old.UID, Home: old.Home})
	}
	pending = append(pending, model.AgentInstallLayout{UID: layout.UID, Home: layout.Home})
	if err := m.repo.TrackInstallLayouts(ctx, n, pending); err != nil {
		return err
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
	desiredSettings := m.installSettings(ctx)
	cfg := agentwire.Config{HubURL: desiredSettings.URL, Token: token, StateDir: base + "/outbox", IntervalSeconds: desiredSettings.Interval, AllowHTTP: desiredSettings.AllowHTTP}
	if err = cfg.Validate(); err != nil {
		return err
	}
	unitName := "talus-agent-" + n.InstallID + ".service"
	binary := base + "/agent"
	config := base + "/config.json"
	unit := unitDir + "/" + unitName
	unitText := "[Unit]\nDescription=Talus monitoring agent\nAfter=network-online.target\nStartLimitIntervalSec=0\n[Service]\nType=simple\nExecStart=" + unitQuote(binary) + " --daemon --config " + unitQuote(config) + "\nRestart=always\nRestartSec=3\nKillMode=control-group\nTimeoutStopSec=10\nUMask=0077\nNoNewPrivileges=true\n[Install]\nWantedBy="
	if uid == 0 {
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
	// A credential change can move the installation to another account. Retire
	// the layout the previous account still runs so two agents cannot report
	// for one server, then record where this install landed: deletion must clean
	// this account's unit and state even after a later credential switch.
	for _, old := range retirements {
		retireCtx, cancelRetire := context.WithTimeout(ctx, 30*time.Second)
		err := m.uninstallLayout(retireCtx, n.InstallID, old, lease, true)
		cancelRetire()
		if err != nil {
			return err
		}
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	err = m.repo.SetInstallLayout(storeCtx, n, uid, layout.Home)
	cancel()
	if err != nil {
		return err
	}
	return lease.releaseAndWait()
}

func residentActivationScript(binary, config, unit, unitName, next, ctl string, attempts int) string {
	q := shellQuote
	return "set -eu\nhad_previous=0\nactivated=0\n" +
		"finish() { rc=$?; set +e; trap - EXIT; if test \"$rc\" != 0 && test \"$activated\" = 1; then " + ctl + " stop " + q(unitName) + " || true; if test \"$had_previous\" = 1; then mv -f " + q(binary+".previous") + " " + q(binary) + "; mv -f " + q(config+".previous") + " " + q(config) + "; if test -f " + q(unit+".previous") + "; then mv -f " + q(unit+".previous") + " " + q(unit) + "; fi; " + ctl + " daemon-reload || true; " + ctl + " restart " + q(unitName) + " || true; fi; fi; rm -f " + q(next) + " " + q(next+".config") + " " + q(next+".unit") + "; exit \"$rc\"; }; trap finish EXIT; trap 'exit 1' HUP INT TERM\n" +
		// Keep the same lock through EXIT rollback. The unit directory survives
		// removal of the installation, so later commands cannot lock a new inode.
		residentLockScript(unit, false) +
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
	_, recorded := recordedLayout(n)
	if recorded && len(n.PendingInstallLayouts) == 0 && n.Deployment == "ready" && n.InstalledHash == desired && n.ConfigHash == config && now.Sub(lastContact) < 90*time.Second {
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

// installAllowed rejects both an active removal and a reconciliation snapshot
// taken before a removal began or finished. The final admission check uses the
// same mutex, so a prepared deletion cannot race a newly admitted installer.
func (m *ResidentMonitor) installAllowed(id uint, epoch uint64) bool {
	m.installJobsMu.Lock()
	defer m.installJobsMu.Unlock()
	return m.removing[id] == nil && m.installEpoch == epoch
}

// errRemovalInProgress reports that another deletion already owns the removal
// fence. A joining request must not invalidate the connection pool: the owner
// is still using its pinned SSH connection for the uninstall.
var errRemovalInProgress = errors.New("agent removal already in progress")

func (m *ResidentMonitor) fenceRemoval(ctx context.Context, id uint) (func(), <-chan struct{}, error) {
	for {
		m.installJobsMu.Lock()
		if pending := m.removing[id]; pending != nil {
			m.installJobsMu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, nil, errors.Join(errRemovalInProgress, ctx.Err())
			}
		}
		if err := ctx.Err(); err != nil {
			m.installJobsMu.Unlock()
			return nil, nil, err
		}
		pending := make(chan struct{})
		m.removing[id] = pending
		m.installEpoch++
		job, busy := m.installJobs[id]
		m.installJobsMu.Unlock()
		if busy {
			job.cancel()
		}
		var once sync.Once
		finish := func() {
			once.Do(func() {
				m.installJobsMu.Lock()
				delete(m.removing, id)
				m.installEpoch++
				close(pending)
				m.installJobsMu.Unlock()
				m.NotifyTopologyChange()
			})
		}
		return finish, job.done, nil
	}
}

type residentRemoval struct {
	monitor *ResidentMonitor
	id      uint
	node    model.AgentNode
	lease   *sshLease
	err     error
	// owned records that this attempt acquired the removal fence. A request that
	// merely waited for another deletion must not invalidate the pool.
	owned  bool
	finish func()
	once   sync.Once
}

// Owned reports whether this request acquired the removal fence and pinned the
// connection whose cleanup it is responsible for.
func (r *residentRemoval) Owned() bool { return r.owned }

// PrepareAgentRemoval fences and joins local installers, then pins a connection
// while the server and its route are still live. It performs no remote removal
// or credential deletion: Remove is called only after the server transaction
// commits. A preparation error is retained so an unreachable host does not
// prevent deletion, and Close always releases the fence on a rejected delete.
func (m *ResidentMonitor) PrepareAgentRemoval(ctx context.Context, id uint) preparedAgentRemoval {
	r := &residentRemoval{monitor: m, id: id}
	var done <-chan struct{}
	r.finish, done, r.err = m.fenceRemoval(ctx, id)
	if r.err != nil {
		return r
	}
	r.owned = true
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			r.err = ctx.Err()
			return r
		}
	}
	r.node, r.err = m.repo.Get(ctx, id)
	if errors.Is(r.err, gorm.ErrRecordNotFound) {
		r.err = nil
		return r
	}
	if r.err == nil && m.deployer != nil {
		// Removal must not depend on the callback URL: an operator can clear
		// AGENT_HUB_URL and still expect the already-installed agent to be
		// uninstalled when the server is deleted.
		r.lease, r.err = m.deployer.ssh.AcquireLease(ctx, id, LeaseConsumerAgent)
	}
	return r
}

func (r *residentRemoval) Remove(ctx context.Context) error {
	err := r.err
	if err == nil && r.lease != nil {
		err = r.monitor.uninstall(ctx, r.node, r.lease)
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(err, r.monitor.repo.Delete(storeCtx, r.id))
}

func (r *residentRemoval) Close() {
	r.once.Do(func() {
		if r.lease != nil {
			r.lease.Release()
		}
		if r.finish != nil {
			r.finish()
		}
	})
}

// agentLayout is one resident-agent installation layout: the account that owns
// it, where its state lives, and the service-manager command that controls it.
type agentLayout struct {
	OS      string
	UID     int
	Home    string
	Base    string
	UnitDir string
	// ctl overrides the service-manager command; tests inject a fake here.
	ctl string
}

func agentLayoutFor(installID string, uid int, home string) agentLayout {
	home = path.Clean(home)
	if uid == 0 {
		return agentLayout{UID: 0, Home: home, Base: "/var/lib/talus-agent/" + installID, UnitDir: "/etc/systemd/system"}
	}
	return agentLayout{
		UID:     uid,
		Home:    home,
		Base:    home + "/.local/share/talus-agent/" + installID,
		UnitDir: home + "/.config/systemd/user",
	}
}

// Control is the service-manager command that owns this layout's unit.
func (l agentLayout) Control() string {
	if l.ctl != "" {
		return l.ctl
	}
	if l.UID == 0 {
		return "systemctl"
	}
	return "systemctl --user"
}

// validInstallHome accepts the absolute home directory reported by the target
// account before it is embedded in remote shell commands.
func validInstallHome(home string) bool {
	if len(home) == 0 || len(home) > 512 || home[0] != '/' || strings.ContainsAny(home, "\r\n\x00") {
		return false
	}
	for _, part := range strings.Split(home, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// nodeForLayout builds the minimal node a layout-directed uninstall needs.
func nodeForLayout(installID string, l agentLayout) model.AgentNode {
	uid, home := l.UID, l.Home
	return model.AgentNode{InstallID: installID, InstallOS: l.OS, InstallUID: &uid, InstallHome: &home}
}

// recordedLayout reports the layout of the last completed installation.
func recordedLayout(n model.AgentNode) (agentLayout, bool) {
	if n.InstallOS == "windows" && n.InstallHome != nil && agentwindows.ValidHome(*n.InstallHome) {
		return windowsAgentLayout(n.InstallID, *n.InstallHome), true
	}
	if n.InstallOS != "" && n.InstallOS != "linux" {
		return agentLayout{}, false
	}
	if n.InstallUID == nil || n.InstallHome == nil || *n.InstallUID < 0 || !validInstallHome(*n.InstallHome) {
		return agentLayout{}, false
	}
	return agentLayoutFor(n.InstallID, *n.InstallUID, *n.InstallHome), true
}

func sameInstallLayout(a, b agentLayout) bool {
	return a.OS == b.OS && a.UID == b.UID && a.Base == b.Base && a.UnitDir == b.UnitDir
}

// residentInstallRetirements separates another account's service from an alias
// of the current installation. A UID has one user manager and one unit with
// this name: stopping an old HOME after activation would also stop the new unit.
// Resolve directory identity before any remote writes, including symlink and
// bind-mount aliases that lexical path cleaning cannot recognize.
func residentInstallRetirements(ctx context.Context, lease *sshLease, current agentLayout, previous []agentLayout) ([]agentLayout, error) {
	var retirements []agentLayout
	for _, old := range previous {
		if sameInstallLayout(old, current) {
			continue
		}
		if old.UID != 0 && current.UID != 0 {
			sameHome := old.Home == current.Home
			if !sameHome {
				result, err := lease.Exec(ctx, "test "+shellQuote(old.Home)+" -ef "+shellQuote(current.Home), 5*time.Second)
				if err != nil {
					return nil, err
				}
				if result == nil || result.ExitCode > 1 {
					return nil, errors.New("cannot compare resident installation directories")
				}
				sameHome = result.ExitCode == 0
			}
			if sameHome {
				if old.UID != current.UID {
					return nil, errors.New("different agent accounts share the same installation directory")
				}
				continue
			}
			if old.UID == current.UID {
				return nil, errors.New("cannot move a resident agent to a different home for the same account")
			}
		}
		retirements = append(retirements, old)
	}
	return retirements, nil
}

// knownInstallLayouts includes unfinished migrations, including a new service
// activated before retirement or the final metadata write failed.
func knownInstallLayouts(n model.AgentNode) []agentLayout {
	var layouts []agentLayout
	if current, ok := recordedLayout(n); ok {
		layouts = append(layouts, current)
	}
	for _, pending := range n.PendingInstallLayouts {
		current := model.AgentNode{InstallID: n.InstallID, InstallOS: pending.OS, InstallUID: &pending.UID, InstallHome: &pending.Home}
		layout, ok := recordedLayout(current)
		if !ok {
			continue
		}
		duplicate := false
		for _, existing := range layouts {
			if sameInstallLayout(existing, layout) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			layouts = append(layouts, layout)
		}
	}
	return layouts
}

// uninstall stops the unit and removes the installation directory. The layout
// follows the account that installed the agent, not the account the current
// credential logs in as: switching the credential to another user must not
// leave the original account's unit and state behind.
func (m *ResidentMonitor) uninstall(ctx context.Context, n model.AgentNode, lease *sshLease) error {
	layouts := knownInstallLayouts(n)
	if len(layouts) == 0 {
		var err error
		layouts, err = discoverInstallLayouts(ctx, n, lease)
		if err != nil {
			return err
		}
	}
	var failures []error
	for _, layout := range layouts {
		if err := m.uninstallLayout(ctx, n.InstallID, layout, lease, false); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (m *ResidentMonitor) uninstallLayout(ctx context.Context, installID string, layout agentLayout, lease *sshLease, strict bool) error {
	if layout.OS == "windows" {
		return m.uninstallWindows(ctx, installID, layout, lease)
	}
	probe := `id -u; printf '%s\n' "$HOME"`
	if layout.UID != 0 {
		// Resolve the owner's name so a cross-account stop can reach that
		// account's service manager; an unknown uid is not an error here.
		probe += "; id -un " + strconv.Itoa(layout.UID) + " 2>/dev/null || true"
	}
	currentUID, owner := -1, ""
	if result, probeErr := lease.Exec(ctx, probe, 5*time.Second); probeErr == nil && result != nil && result.ExitCode == 0 {
		lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
		if len(lines) >= 2 {
			if uid, convErr := strconv.Atoi(lines[0]); convErr == nil {
				currentUID = uid
			}
		}
		if len(lines) >= 3 {
			owner = strings.TrimSpace(lines[2])
		}
	}
	// A user unit is only reachable through its owner's service manager. Root
	// can switch to that account; any other login cannot use its manager.
	runAs, manage := "", true
	switch {
	case currentUID < 0:
		manage = layout.UID == 0
	case layout.UID == currentUID:
	case currentUID == 0 && owner != "":
		runAs = owner
	default:
		manage = false
	}
	if strict && !manage {
		return errors.New("cannot stop previous agent account's service")
	}
	if layout.UID != 0 && (currentUID < 0 || currentUID == 0 && owner == "") {
		return errors.New("cannot safely clean resident installation without its account")
	}
	unitName := "talus-agent-" + installID + ".service"
	result, err := lease.Exec(ctx, residentRemovalScript(layout, unitName, runAs, manage, strict), 30*time.Second)
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
// unit or installation directory is not an error. The stable lock file remains
// outside the removed directory so waiting activations keep the same lock.
//
// Locking is best effort on removal: a host installed by an older release may
// lack flock, and a host whose unit directory is already gone has nothing to
// serialize. A lock that is present but held still fails the uninstall instead
// of racing a remote activation/rollback.
//
// runAs switches the entire cleanup to the account that owns a user unit,
// including locking and file removal. Root must not perform filesystem writes
// through paths controlled by the previous, unprivileged installation owner.
// manage=false skips service-manager calls for a different unprivileged login.
func residentUninstallScript(l agentLayout, unitName, runAs string, manage bool) string {
	return residentRemovalScript(l, unitName, runAs, manage, false)
}

func residentRemovalScript(l agentLayout, unitName, runAs string, manage, strict bool) string {
	script := residentRemovalAccountScript(l, unitName, manage, strict)
	if runAs == "" {
		return script
	}
	return "set -eu\nrunuser -u " + shellQuote(runAs) + " -- env XDG_RUNTIME_DIR=" +
		shellQuote("/run/user/"+strconv.Itoa(l.UID)) + " sh -c " + shellQuote(script) + "\n"
}

func residentRemovalAccountScript(l agentLayout, unitName string, manage, strict bool) string {
	q := shellQuote
	unit := l.UnitDir + "/" + unitName
	script := "set -eu\n" + residentLockScript(unit, true)
	removeUnit := "rm -f " + q(unit) + " " + q(unit+".previous") + "\n"
	if !manage {
		return script + removeUnit + "rm -rf " + q(l.Base) + "\n"
	}
	ctl := l.Control()
	stop := ctl + " disable --now " + q(unitName) + " >/dev/null 2>&1 || true\n"
	ignoreFailure := " || true"
	if strict {
		ignoreFailure = ""
		// A previously retired unit may already be absent on a retry. Accept a
		// failed disable only if its manager explicitly confirms it is inactive
		// (3) or unknown (4), never when the manager itself cannot be reached.
		stop = "if " + ctl + " disable --now " + q(unitName) + " >/dev/null 2>&1; then :; else\n" +
			"  stop_state=0; " + ctl + " is-active --quiet " + q(unitName) + " >/dev/null 2>&1 || stop_state=$?\n" +
			"  case \"$stop_state\" in 3|4) ;; *) exit 1 ;; esac\nfi\n"
	}
	return script + stop +
		removeUnit +
		ctl + " daemon-reload >/dev/null 2>&1" + ignoreFailure + "\n" +
		"rm -rf " + q(l.Base) + "\n"
}

// residentLockScript never truncates or writes the lock's contents. A root
// cleanup can encounter a lock in another user's writable directory: rejecting
// links before and after opening avoids using a known unsafe lock, while the
// non-truncating open also preserves a target substituted between those checks.
// Keep the lock inode after removal so an activation's rollback stays serialized.
func residentLockScript(unit string, optional bool) string {
	lock := shellQuote(unit + ".lock")
	script := "if test -L " + lock + " || { test -e " + lock + " && ! test -f " + lock + "; }; then exit 1; fi\n" +
		"exec 9<> " + lock + "\n" +
		"if test -L " + lock + " || ! test -f " + lock + "; then exit 1; fi\n" +
		"flock -w 10 9\n"
	if optional {
		return "if command -v flock >/dev/null 2>&1 && test -d " + shellQuote(path.Dir(unit)) + "; then\n" + script + "fi\n"
	}
	return script
}
