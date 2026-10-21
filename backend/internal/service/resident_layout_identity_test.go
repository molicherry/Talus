package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestResidentLayoutMigrationHomeAliasesKeepActiveInstallation(t *testing.T) {
	for _, scenario := range []string{"current-alias", "recorded-alias", "both-aliases"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "real home")
			alias, secondAlias := filepath.Join(dir, "alias"), filepath.Join(dir, "second-alias")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			for _, link := range []string{alias, secondAlias} {
				if err := os.Symlink(home, link); err != nil {
					t.Fatal(err)
				}
			}
			oldHome, currentHome := home, alias
			if scenario == "recorded-alias" {
				oldHome, currentHome = alias, home
			} else if scenario == "both-aliases" {
				oldHome, currentHome = alias, secondAlias
			}
			f := newResidentMigrationFixture(t, 1001, oldHome, 1001, currentHome)
			base := filepath.Join(currentHome, ".local/share/talus-agent", f.node.InstallID)
			unit := filepath.Join(currentHome, ".config/systemd/user", "talus-agent-"+f.node.InstallID+".service")
			outbox := filepath.Join(base, "outbox", "unacknowledged-sample")
			if err := os.MkdirAll(filepath.Dir(outbox), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outbox, []byte("pending sample"), 0600); err != nil {
				t.Fatal(err)
			}
			ctl := filepath.Join(dir, "systemctl")
			if err := os.WriteFile(ctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			f.remote.setActivation(func(_ string, ch ssh.Channel) {
				if err := os.MkdirAll(filepath.Dir(unit), 0700); err != nil {
					t.Error(err)
					exitStatus(ch, 1)
					return
				}
				for _, file := range []string{filepath.Join(base, "agent"), filepath.Join(base, "config.json"), unit} {
					if err := os.WriteFile(file, []byte("healthy active installation"), 0600); err != nil {
						t.Error(err)
						exitStatus(ch, 1)
						return
					}
				}
				exitStatus(ch, 0)
			})
			f.remote.setRetirement(func(command string, ch ssh.Channel) {
				cmd := exec.Command("sh", "-c", strings.ReplaceAll(command, "systemctl --user", shellQuote(ctl)))
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				if err := cmd.Run(); err != nil {
					t.Error(err)
					exitStatus(ch, 1)
					return
				}
				exitStatus(ch, 0)
			})
			after := f.install(t, f.node)
			assertResidentMigrationReady(t, after, 1001, currentHome)
			if f.remote.retirements.Load() != 0 {
				t.Fatal("HOME alias retired the active installation")
			}
			for _, file := range []string{filepath.Join(base, "agent"), filepath.Join(base, "config.json"), unit, outbox} {
				if _, err := os.Stat(file); err != nil {
					t.Fatalf("ready migration removed active file %s: %v", file, err)
				}
			}
			if data, err := os.ReadFile(outbox); err != nil || string(data) != "pending sample" {
				t.Fatalf("HOME alias lost pending samples: %q %v", data, err)
			}
		})
	}
}

func TestResidentLayoutMigrationRejectsUnsafeHomeChangesBeforeWriting(t *testing.T) {
	for _, scenario := range []string{"same-uid-different-home", "different-uids-aliased-home"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			oldHome, newHome := filepath.Join(dir, "old"), filepath.Join(dir, "new")
			if err := os.Mkdir(oldHome, 0700); err != nil {
				t.Fatal(err)
			}
			newUID := 1001
			if scenario == "different-uids-aliased-home" {
				newUID = 1002
				if err := os.Symlink(oldHome, newHome); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(newHome, 0700); err != nil {
				t.Fatal(err)
			}
			f := newResidentMigrationFixture(t, 1001, oldHome, newUID, newHome)
			after := f.install(t, f.node)
			assertResidentMigrationFailed(t, after)
			if f.remote.setups.Load() != 0 || f.remote.activations.Load() != 0 || f.remote.retirements.Load() != 0 || len(f.remote.files.fileKeys()) != 0 {
				t.Fatal("unsafe HOME migration changed remote files or services")
			}
			if after.InstallUID == nil || *after.InstallUID != 1001 || after.InstallHome == nil || *after.InstallHome != oldHome || len(after.PendingInstallLayouts) != 0 {
				t.Fatalf("rejected migration changed cleanup locations: %+v", after)
			}
		})
	}
}

func TestResidentInstallRetirementsRejectsFailedDirectoryComparison(t *testing.T) {
	address, key := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		exitStatus(ch, 2)
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := sshSvc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	current := agentLayoutFor("install1", 1001, "/new")
	previous := []agentLayout{agentLayoutFor("install1", 1002, "/old")}
	if layouts, err := residentInstallRetirements(context.Background(), lease, current, previous); err == nil || len(layouts) != 0 {
		t.Fatalf("failed directory comparison accepted a retirement: %+v %v", layouts, err)
	}
}

func TestResidentUninstallDoesNotCleanUserFilesWithUnknownAccount(t *testing.T) {
	for _, probe := range []string{"0\n/root\n", "invalid uid\n/root\n"} {
		t.Run(strings.TrimSpace(strings.SplitN(probe, "\n", 2)[0]), func(t *testing.T) {
			commands := make(chan string, 2)
			address, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
				commands <- command
				if _, err := ch.Write([]byte(probe)); err != nil {
					t.Error(err)
				}
				exitStatus(ch, 0)
			})
			pool := newTestPool(t)
			seedPool(t, pool, 1, dialTestSSH(t, address, key))
			sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			lease, err := sshSvc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			monitor := NewResidentMonitor(nil, nil, nil, "", false, 5)
			if err := monitor.uninstallLayout(context.Background(), "install1", agentLayoutFor("install1", 1001, "/home/alice"), lease, false); err == nil {
				t.Fatal("cleanup accepted an unresolved user account")
			}
			if len(commands) != 1 {
				t.Fatalf("unresolved account executed cleanup instead of only probing: %d commands", len(commands))
			}
		})
	}
}
