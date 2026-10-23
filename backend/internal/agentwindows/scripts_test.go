package agentwindows

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeCommand(t *testing.T, command string) string {
	t.Helper()
	parts := strings.Fields(command)
	b, err := base64.StdEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func TestWindowsCommandQuotingAndLength(t *testing.T) {
	text := "C:/部署 ' data/$literal; & file"
	script := "[Console]::WriteLine(" + Quote(text) + ")"
	if got := decodeCommand(t, Command(script)); got != script {
		t.Fatalf("UTF-16 roundtrip = %q", got)
	}
	l := Layout{Home: "C:/Program Data/部署 ' data", InstallID: strings.Repeat("a", 32), Attempt: strings.Repeat("b", 32)}
	for _, script := range []string{Probe, Context, l.Prepare(), l.Launch(strings.Repeat("c", 64)), l.Wait(70), l.RemoveDetached()} {
		if n := len(Command(script)); n > 8000 {
			t.Fatalf("command exceeds cmd.exe limit: %d", n)
		}
	}
}

func TestWindowsRejectsUnsafeInstallationHomes(t *testing.T) {
	for _, p := range []string{"", "/tmp", "//host/share", "C:relative", "C:/Data/../Windows", "C:/Data:stream", "C:/Data\n", "C:/Data/.", "C:/Data ", "C:/Data*", `C:\ProgramData`} {
		if ValidHome(p) {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"C:/ProgramData", "D:/Data Space", "C:/部署 ' data"} {
		if !ValidHome(p) {
			t.Errorf("rejected %q", p)
		}
	}
}

// Parse every shipped script with PowerShell's own parser, and execute the
// compressed command transport. Works on Linux with pwsh and natively on Windows.
func TestWindowsPowerShellSyntaxAndTransport(t *testing.T) {
	ps, err := exec.LookPath("pwsh")
	if err != nil {
		ps, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	l := Layout{Home: "C:/Program Data/部署 ' data", InstallID: "test-id", Attempt: "test-attempt"}
	for _, script := range []string{Probe, Context, l.Prepare(), l.Activate("a", "b"), l.Launch("c"), l.Wait(1), l.Remove(), l.RemoveDetached()} {
		check := "$e=$null;$t=$null;[void][Management.Automation.Language.Parser]::ParseInput(" + Quote(script) + ",[ref]$t,[ref]$e);if($e.Count){$e|Out-String|Write-Output;exit 1}"
		args := strings.Fields(Command(check))[1:]
		if out, err := exec.Command(ps, args...).CombinedOutput(); err != nil {
			t.Fatalf("PowerShell parse: %v %s", err, out)
		}
	}
	text := "部署 ' literal $value & ;"
	script := strings.Repeat("# long script padding\n", 300) + "[Console]::WriteLine(" + Quote(text) + ")"
	args := strings.Fields(Command(script))[1:]
	out, err := exec.Command(ps, args...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != text {
		t.Fatalf("compressed command: %v %s", err, out)
	}
}
