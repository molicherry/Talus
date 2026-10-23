// Package agentwindows contains the PowerShell deployment contract shared by
// the Linux Hub and the native Windows acceptance tests.
package agentwindows

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path"
	"strings"
	"unicode/utf16"
)

//go:embed common.ps1
var common string

//go:embed prepare.ps1
var prepare string

//go:embed activate.ps1
var activate string

//go:embed launch.ps1
var launch string

//go:embed remove.ps1
var remove string

const Probe = `$ErrorActionPreference='Stop'
if ([Environment]::OSVersion.Platform -ne 'Win32NT') { exit 1 }
$arch=$env:PROCESSOR_ARCHITEW6432
if (!$arch) { $arch=$env:PROCESSOR_ARCHITECTURE }
[Console]::WriteLine('TALUS_WINDOWS_V1')
[Console]::WriteLine($arch)
`

const Context = `$ErrorActionPreference='Stop'
$admin=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
@{home=[Environment]::GetFolderPath('CommonApplicationData');admin=$admin} | ConvertTo-Json -Compress
`

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Command works with both cmd.exe and PowerShell as the OpenSSH default shell.
// Long scripts are compressed to stay below cmd.exe's 8191-character limit.
func Command(script string) string {
	if len(utf16.Encode([]rune(script))) > 2400 {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, _ = w.Write([]byte(script))
		_ = w.Close()
		script = "$m=New-Object IO.MemoryStream(,[Convert]::FromBase64String(" + Quote(base64.StdEncoding.EncodeToString(b.Bytes())) + ")); $z=New-Object IO.Compression.GzipStream($m,[IO.Compression.CompressionMode]::Decompress); $r=New-Object IO.StreamReader($z); & ([ScriptBlock]::Create($r.ReadToEnd()))"
	}
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

func ValidHome(home string) bool {
	if len(home) < 3 || len(home) > 240 || home[1] != ':' || home[2] != '/' || !((home[0] >= 'A' && home[0] <= 'Z') || (home[0] >= 'a' && home[0] <= 'z')) {
		return false
	}
	if strings.ContainsAny(home[2:], "\\\r\n\x00:*?\"<>|") {
		return false
	}
	for _, part := range strings.Split(home[3:], "/") {
		if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return path.Clean(home) == home
}

type Layout struct{ Home, InstallID, Attempt string }

func (l Layout) Root() string  { return l.Home + "/Talus-Agent" }
func (l Layout) Base() string  { return l.Root() + "/" + l.InstallID }
func (l Layout) Stage() string { return l.Base() + "/staging/" + l.Attempt }
func (l Layout) Name() string  { return "Talus-Agent-" + l.InstallID }
func (l Layout) vars() string {
	return "$ErrorActionPreference='Stop'\n$ProgressPreference='SilentlyContinue'\n" +
		"$root=" + Quote(l.Root()) + "\n$base=" + Quote(l.Base()) + "\n$stage=" + Quote(l.Stage()) + "\n$name=" + Quote(l.Name()) + "\n$task=" + Quote(l.Name()+"-Deploy-"+l.Attempt) + "\n"
}
func (l Layout) Prepare() string { return l.vars() + common + prepare }
func (l Layout) Activate(binaryHash, configHash string) string {
	return l.vars() + "$binaryHash=" + Quote(binaryHash) + "\n$configHash=" + Quote(configHash) + "\n" + common + activate
}

// Windows PowerShell 5.1 needs a BOM to read non-ASCII installation paths in a
// script file as UTF-8. Commands sent through SSH are already UTF-16 encoded.
func (l Layout) ActivationFile(binaryHash, configHash string) []byte {
	return []byte("\xef\xbb\xbf" + l.Activate(binaryHash, configHash))
}
func (l Layout) Launch(scriptHash string) string {
	return l.vars() + "$scriptHash=" + Quote(scriptHash) + "\n" + launch
}
func (l Layout) Remove() string { return l.vars() + common + remove }

// RemoveDetached lets a committed Hub deletion finish cleanup even if an
// activation still holds the lock or the SSH/request cleanup budget expires.
// No server credential is needed once the local SYSTEM task has been scheduled.
func (l Layout) RemoveDetached() string {
	cleanup := l.vars() + common + "try {\n" + remove + "\n} finally { Unregister-ScheduledTask -TaskName ($name+'-Remove') -Confirm:$false -ErrorAction SilentlyContinue; Remove-Item -LiteralPath (Join-Path $root ($name+'.remove.ps1')) -Force -ErrorAction SilentlyContinue }\n"
	return l.vars() + common + `Assert-Admin
Assert-Path $root
if (!(Test-Path -LiteralPath $root) -and !(Get-Service -Name $name -ErrorAction SilentlyContinue)) { exit 0 }
Protect-Directory $root
$script=Join-Path $root ($name+'.remove.ps1')
Assert-Path $script
` + "[IO.File]::WriteAllText($script," + Quote(cleanup) + ",(New-Object Text.UTF8Encoding($true)))\n" + `$task=$name+'-Remove'
$exe=Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$action=New-ScheduledTaskAction -Execute $exe -Argument ('-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$script+'"')
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 5) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
$null=Register-ScheduledTask -TaskName $task -Action $action -Principal $principal -Settings $settings -Force
Start-ScheduledTask -TaskName $task
`
}
func (l Layout) Wait(attempts int) string {
	return l.vars() + fmt.Sprintf(`for ($i=0; $i -lt %d; $i++) {
  $result=Join-Path $stage 'result.json'
  if (Test-Path -LiteralPath $result) {
    $r=Get-Content -LiteralPath $result -Raw | ConvertFrom-Json
    if ($r.ok) { [Console]::WriteLine('TALUS_READY'); exit 0 }
    exit 1
  }
  Start-Sleep -Seconds 1
}
exit 1
`, attempts)
}

func Hash(file string) string {
	return "$ErrorActionPreference='Stop'; (Get-FileHash -Algorithm SHA256 -LiteralPath " + Quote(file) + ").Hash.ToLowerInvariant()"
}
