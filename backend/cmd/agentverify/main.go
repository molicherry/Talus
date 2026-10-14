// Command agentverify validates the agent artifacts a Hub image ships: the
// manifest, both architectures, sizes, hashes, ELF machines and (optionally) the
// protocol output of the runnable architecture. It is used by CI against the
// files extracted from the built images, and locally against build output.
//
//	agentverify [-run-protocol] <artifacts-dir>
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/vpsmanager/backend/internal/agentproto"
)

func main() {
	runProtocol := flag.Bool("run-protocol", false, "execute the amd64 artifact and validate its JSON protocol fields")
	checkJSON := flag.String("check-json", "", "validate one captured agent report against the manifest")
	versionInBinary := flag.Bool("version-in-binary", false, "require every artifact to embed its manifest build version (for architectures that cannot be executed)")
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

	if *checkJSON != "" {
		// The same strict rule the in-image acceptance applies, so a captured report
		// (for example from the target-architecture binary) can be validated after
		// the fact - and rejected for a fractional, exponent or string protocol.
		raw, err := os.ReadFile(*checkJSON)
		if err != nil {
			fail(err)
		}
		if err := agentproto.VerifyReport(raw, manifest); err != nil {
			fail(err)
		}
		fmt.Printf("report OK: %s matches manifest build_version=%s protocol_version=%d\n",
			*checkJSON, manifest.BuildVersion, manifest.ProtocolVersion)
	}
	if *runProtocol {
		if err := verifyProtocol(dir, manifest); err != nil {
			fail(err)
		}
		fmt.Println("protocol output OK: agent_version and protocol_version match the manifest")
	}
	if *versionInBinary {
		if err := verifyVersionInBinary(dir, manifest); err != nil {
			fail(err)
		}
		fmt.Println("binary version OK: every artifact embeds its manifest build version")
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
	// One rule for both paths: the executed artifact is checked with the same strict
	// comparison the final image uses, against the artifact's own build version.
	expected := *manifest
	expected.BuildVersion = artifact.BuildVersion
	return agentproto.VerifyReport(out, &expected)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "agentverify:", err)
	os.Exit(1)
}

// verifyVersionInBinary checks the build version stamped into each artifact. An
// architecture the host cannot execute (arm64 on an amd64 builder) can still be
// verified this way: the version is injected with -ldflags at build time, so a
// mismatch means the artifact was not built from this source/version.
func verifyVersionInBinary(dir string, manifest *agentproto.Manifest) error {
	for _, artifact := range manifest.Agents {
		raw, err := os.ReadFile(filepath.Join(dir, artifact.File))
		if err != nil {
			return fmt.Errorf("read %s: %w", artifact.File, err)
		}
		if !bytes.Contains(raw, []byte(artifact.BuildVersion)) {
			return fmt.Errorf("%s does not embed build version %q", artifact.File, artifact.BuildVersion)
		}
	}
	return nil
}
