package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResidentScriptsRejectUnsafeLocksWithoutChangingFiles(t *testing.T) {
	for _, operation := range []string{"activation", "uninstall"} {
		for _, unsafe := range []string{"symlink", "dangling-symlink", "directory", "replacement-race"} {
			t.Run(operation+"/"+unsafe, func(t *testing.T) {
				dir := t.TempDir()
				base := filepath.Join(dir, "state")
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				binary, config := filepath.Join(base, "agent"), filepath.Join(base, "config.json")
				unit, next := filepath.Join(dir, "agent.service"), filepath.Join(base, ".next-agent")
				lock, target := unit+".lock", filepath.Join(dir, "unrelated-file")
				called, checkpoint := filepath.Join(dir, "manager-called"), filepath.Join(dir, "replaced")
				ctl := filepath.Join(dir, "systemctl")
				files := map[string]string{
					binary: "old binary", config: "old config", unit: "old unit",
					next: "new binary", next + ".config": "new config", next + ".unit": "new unit",
					ctl: "#!/bin/sh\ntouch " + shellQuote(called) + "\n",
				}
				if unsafe != "dangling-symlink" {
					files[target] = "must remain intact"
				}
				for file, contents := range files {
					if err := os.WriteFile(file, []byte(contents), 0700); err != nil {
						t.Fatal(err)
					}
				}
				switch unsafe {
				case "symlink", "dangling-symlink":
					if err := os.Symlink(target, lock); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(lock, 0700); err != nil {
						t.Fatal(err)
					}
				case "replacement-race":
					if err := os.WriteFile(lock, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				script := residentUninstallScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true)
				if operation == "activation" {
					script = residentActivationScript(binary, config, unit, "agent.service", next, shellQuote(ctl), 1)
				}
				if unsafe == "replacement-race" {
					// Replace the checked path immediately before the open. The open
					// itself must preserve the target even if a prior check was safe.
					script = "test() {\n" +
						"if [ \"$1\" = -e ] && [ \"$2\" = " + shellQuote(lock) + " ] && [ ! -e " + shellQuote(checkpoint) + " ]; then\n" +
						"  touch " + shellQuote(checkpoint) + "; rm -f " + shellQuote(lock) + "; ln -s " + shellQuote(target) + " " + shellQuote(lock) + "; return 1\nfi\n" +
						"command test \"$@\"\n}\n" + script
				}
				if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err == nil {
					t.Fatalf("unsafe lock did not reject %s: %s", operation, out)
				}
				if unsafe == "replacement-race" {
					if _, err := os.Stat(checkpoint); err != nil {
						t.Fatalf("lock replacement did not occur: %v", err)
					}
				}
				if unsafe == "dangling-symlink" {
					if _, err := os.Stat(target); !os.IsNotExist(err) {
						t.Fatalf("unsafe lock created its symlink target: %v", err)
					}
				} else if got, err := os.ReadFile(target); err != nil || string(got) != files[target] {
					t.Fatalf("unsafe lock modified unrelated file: %q %v", got, err)
				}
				for _, file := range []string{binary, config, unit} {
					if got, err := os.ReadFile(file); err != nil || string(got) != files[file] {
						t.Fatalf("rejected %s changed %s: %q %v", operation, file, got, err)
					}
				}
				if _, err := os.Stat(called); !os.IsNotExist(err) {
					t.Fatalf("rejected %s reached the service manager", operation)
				}
			})
		}
	}
}

func TestResidentScriptsPreserveExistingLockContents(t *testing.T) {
	for _, hardLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "hard-link"}[hardLink], func(t *testing.T) {
			dir := t.TempDir()
			unit, target := filepath.Join(dir, "agent.service"), filepath.Join(dir, "unrelated-file")
			if err := os.WriteFile(target, []byte("must remain intact"), 0600); err != nil {
				t.Fatal(err)
			}
			if hardLink {
				if err := os.Link(target, unit+".lock"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(unit+".lock", []byte("stable lock"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(unit + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			for _, optional := range []bool{false, true} {
				if out, err := exec.Command("sh", "-c", "set -eu\n"+residentLockScript(unit, optional)).CombinedOutput(); err != nil {
					t.Fatalf("lock: %v %s", err, out)
				}
				if after, err := os.ReadFile(unit + ".lock"); err != nil || string(after) != string(before) {
					t.Fatalf("lock contents changed: %q %v", after, err)
				}
			}
		})
	}
}

func TestResidentCrossAccountCleanupDoesNotWriteBeforeSwitchingAccount(t *testing.T) {
	for _, failure := range []string{"1", "127"} {
		t.Run("runuser-exit-"+failure, func(t *testing.T) {
			dir := t.TempDir()
			base, unitDir := filepath.Join(dir, "state"), filepath.Join(dir, "units")
			for _, path := range []string{base, unitDir} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			unit := filepath.Join(unitDir, "agent.service")
			if err := os.WriteFile(unit, []byte("old unit"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "runuser"), []byte("#!/bin/sh\nexit "+failure+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			layout := agentLayout{UID: 1001, Home: dir, Base: base, UnitDir: unitDir}
			cmd := exec.Command("sh", "-c", residentUninstallScript(layout, "agent.service", "alice", true))
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("cleanup succeeded without switching accounts: %s", out)
			}
			if got, err := os.ReadFile(unit); err != nil || string(got) != "old unit" {
				t.Fatalf("failed account switch changed the unit: %q %v", got, err)
			}
			if _, err := os.Stat(base); err != nil {
				t.Fatalf("failed account switch removed the installation: %v", err)
			}
			if _, err := os.Lstat(unit + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("cleanup touched the lock before switching accounts: %v", err)
			}
		})
	}
}

func TestResidentCrossAccountCleanupRunsFileCommandsAsOwner(t *testing.T) {
	dir := t.TempDir()
	base, unitDir := filepath.Join(dir, "state"), filepath.Join(dir, "units")
	for _, path := range []string{base, unitDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls")
	for _, name := range []string{"flock", "rm", "systemctl"} {
		realCommand := "true"
		if name != "systemctl" {
			var err error
			realCommand, err = exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
		}
		body := "#!/bin/sh\n[ \"${TALUS_TEST_ACCOUNT-}\" = alice ] || exit 1\n" +
			"printf '%s\\n' " + shellQuote(name) + " >> " + shellQuote(log) + "\nexec " + shellQuote(realCommand) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	runuser := "#!/bin/sh\n[ \"$1\" = -u ] && [ \"$2\" = alice ] && [ \"$3\" = -- ] || exit 1\n" +
		"shift 3\nTALUS_TEST_ACCOUNT=alice; export TALUS_TEST_ACCOUNT\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "runuser"), []byte(runuser), 0700); err != nil {
		t.Fatal(err)
	}
	layout := agentLayout{UID: 1001, Home: dir, Base: base, UnitDir: unitDir}
	cmd := exec.Command("sh", "-c", residentUninstallScript(layout, "agent.service", "alice", true))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-account cleanup: %v %s", err, out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"flock", "rm", "systemctl"} {
		if !strings.Contains(string(calls), name+"\n") {
			t.Fatalf("cleanup did not run %s as the installation owner: %s", name, calls)
		}
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("cross-account cleanup retained the installation: %v", err)
	}
}
