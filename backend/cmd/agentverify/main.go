// Command agentverify validates the agent artifacts a Hub image ships: the
// manifest, both architectures, sizes, hashes, ELF machines and (optionally) the
// protocol output of the runnable architecture. It is used by CI against the
// files extracted from the built images, and locally against build output.
//
//	agentverify [-run-protocol] <artifacts-dir>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/vpsmanager/backend/internal/agentproto"
)

func main() {
	runProtocol := flag.Bool("run-protocol", false, "execute the amd64 artifact and validate its JSON protocol fields")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: agentverify [-run-protocol] <artifacts-dir>")
		os.Exit(2)
	}
	dir := flag.Arg(0)

	manifest, err := agentproto.LoadManifest(dir)
	if err != nil {
		fail(err)
	}
	if err := manifest.Verify(dir); err != nil {
		fail(err)
	}
	fmt.Printf("artifacts OK: build_version=%s protocol_version=%d agents=%d\n",
		manifest.BuildVersion, manifest.ProtocolVersion, len(manifest.Agents))

	if *runProtocol {
		if err := verifyProtocol(dir, manifest); err != nil {
			fail(err)
		}
		fmt.Println("protocol output OK: agent_version and protocol_version match the manifest")
	}
}

// verifyProtocol runs the amd64 artifact (the architecture CI can execute) and
// checks that its JSON output carries the manifest's versions.
func verifyProtocol(dir string, manifest *agentproto.Manifest) error {
	var artifact agentproto.Artifact
	for _, candidate := range manifest.Agents {
		if candidate.Platform == "linux/amd64" {
			artifact = candidate
			break
		}
	}
	if artifact.File == "" {
		return fmt.Errorf("manifest has no linux/amd64 artifact to execute")
	}
	out, err := exec.Command(filepath.Join(dir, artifact.File), "--format", "json").Output()
	if err != nil {
		return fmt.Errorf("run %s: %w", artifact.File, err)
	}
	var decoded struct {
		AgentVersion    string `json:"agent_version"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		return fmt.Errorf("decode %s output: %w", artifact.File, err)
	}
	if decoded.AgentVersion != artifact.BuildVersion || decoded.AgentVersion == "" {
		return fmt.Errorf("agent_version %q does not match manifest %q", decoded.AgentVersion, artifact.BuildVersion)
	}
	if decoded.ProtocolVersion != manifest.ProtocolVersion {
		return fmt.Errorf("protocol_version %d does not match manifest %d", decoded.ProtocolVersion, manifest.ProtocolVersion)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "agentverify:", err)
	os.Exit(1)
}
