package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

const residentLayoutDiscoveryLimit = 1024

// discoverInstallLayouts locates existing legacy installations without assuming
// that the current SSH account is the one that installed the agent. It does not
// modify either the host or the stored credential. Root also checks passwd homes;
// an unprivileged account checks its own home and the system layout only.
func discoverInstallLayouts(ctx context.Context, n model.AgentNode, lease *sshLease) ([]agentLayout, error) {
	if !validDiscoveryInstallID(n.InstallID) {
		return nil, errors.New("cannot discover resident layout: invalid installation id")
	}
	result, err := lease.Exec(ctx, residentLayoutDiscoveryScript(n.InstallID), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("discover resident installation layouts: %w", err)
	}
	if result == nil || result.ExitCode != 0 || result.StdoutTruncated || result.OutputTruncated {
		// Windows has a single machine-wide layout, independent of SSH account.
		if layout, winErr := discoverWindowsLayout(ctx, n.InstallID, lease); winErr == nil {
			return []agentLayout{layout}, nil
		}
		return nil, errors.New("resident installation layout discovery did not complete")
	}
	return parseDiscoveredInstallLayouts(n, result.Stdout)
}

func validDiscoveryInstallID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// The NUL-delimited protocol preserves spaces, quotes and tabs in home paths.
// The final marker records whether all passwd homes were checked. A missing
// getent result or account limit cannot look like a successful empty discovery.
func residentLayoutDiscoveryScript(installID string) string {
	q := shellQuote
	return "set -eu\ninstall_id=" + q(installID) + "\n" +
		"printf 'TALUS_LAYOUTS_V1\\000'\n" +
		`probe_layout() {
  probe_uid=$1
  probe_home=$2
  case "$probe_uid" in ''|*[!0-9]*) return 0 ;; esac
  case "$probe_home" in /*) ;; *) return 0 ;; esac
  case "/$probe_home/" in */../*) return 0 ;; esac
  if test "$probe_uid" = 0; then
    probe_base=/var/lib/talus-agent/$install_id
    probe_unit=/etc/systemd/system/talus-agent-$install_id.service
  else
    probe_base=$probe_home/.local/share/talus-agent/$install_id
    probe_unit=$probe_home/.config/systemd/user/talus-agent-$install_id.service
  fi
  if test -e "$probe_base" || test -L "$probe_base" || test -e "$probe_unit" || test -L "$probe_unit"; then
    printf '%s\000%s\000' "$probe_uid" "$probe_home"
  fi
}
current_uid=$(id -u)
case "$current_uid" in ''|*[!0-9]*) exit 1 ;; esac
probe_layout 0 /root
probe_layout "$current_uid" "${HOME-}"
if test "$current_uid" = 0; then
  scan_accounts() {
    account_count=0
    complete=0
    while IFS=: read -r account_name account_password account_uid account_gid account_gecos account_home account_shell; do
      if test "$account_name" = TALUS_PASSWD_COMPLETE && test -z "$account_uid"; then
        complete=1
        break
      fi
      account_count=$((account_count + 1))
      if test "$account_count" -gt ` + strconv.Itoa(residentLayoutDiscoveryLimit) + `; then return 1; fi
      probe_layout "$account_uid" "$account_home"
    done
    test "$complete" = 1
  }
  if command -v getent >/dev/null 2>&1; then
    { getent passwd && printf '%s\n' TALUS_PASSWD_COMPLETE; } | scan_accounts
  else
    { cat /etc/passwd && printf '%s\n' TALUS_PASSWD_COMPLETE; } | scan_accounts
  fi
  printf 'ROOT_DONE\000'
else
  printf 'USER_DONE\000'
fi
`
}

func parseDiscoveredInstallLayouts(n model.AgentNode, output string) ([]agentLayout, error) {
	parts := strings.Split(output, "\x00")
	if len(parts) < 3 || parts[0] != "TALUS_LAYOUTS_V1" || parts[len(parts)-1] != "" || (len(parts)-3)%2 != 0 {
		return nil, errors.New("invalid resident layout discovery response")
	}
	scope := parts[len(parts)-2]
	if scope != "ROOT_DONE" && scope != "USER_DONE" {
		return nil, errors.New("incomplete resident layout discovery response")
	}
	if len(parts) > 2*(residentLayoutDiscoveryLimit+2)+3 {
		return nil, errors.New("resident layout discovery exceeded its account limit")
	}
	seen := make(map[string]bool)
	var layouts []agentLayout
	for i := 1; i < len(parts)-2; i += 2 {
		uid, err := strconv.Atoi(parts[i])
		if err != nil || uid < 0 || !validInstallHome(parts[i+1]) {
			return nil, errors.New("invalid account in resident layout discovery response")
		}
		layout := agentLayoutFor(n.InstallID, uid, path.Clean(parts[i+1]))
		key := strconv.Itoa(uid) + ":" + path.Clean(layout.Base)
		if !seen[key] {
			seen[key] = true
			layouts = append(layouts, layout)
		}
	}
	// A complete root scan can safely confirm that a manually removed agent
	// has no remaining layout. An unprivileged scan cannot exclude another
	// account's old installation when the stored row predates layout tracking.
	if scope == "USER_DONE" && len(layouts) == 0 && (n.Deployment == "ready" || n.InstalledHash != "") {
		return nil, errors.New("previously installed resident agent layout could not be located")
	}
	return layouts, nil
}
