// Package agentwindows contains the PowerShell deployment contract shared by
// the Linux Hub and the native Windows acceptance tests.
package agentwindows

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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

//go:embed fetch.ps1
var fetch string

//go:embed activate.ps1
var activate string

//go:embed launch.ps1
var launch string

//go:embed remove.ps1
var remove string

//go:embed removal_result.ps1
var removalResult string

const ActivationTaskSeconds = 180
const ActivationWaitSeconds = ActivationTaskSeconds + 20

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

// A PowerShell 7 SSH shell can pass its incompatible module directories to
// powershell.exe. Deployment only needs the executing edition's system modules.
const systemModules = "$env:PSModulePath=$PSHOME+'/Modules';\n"

// Command works with both cmd.exe and PowerShell as the OpenSSH default shell.
// Long scripts are compressed to stay below cmd.exe's 8191-character limit.
func Command(script string) string {
	script = strings.ReplaceAll(script, "\r\n", "\n")
	if len(utf16.Encode([]rune(script))) > 2400 {
		var b bytes.Buffer
		w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = w.Write([]byte(script))
		_ = w.Close()
		script = "$m=New-Object IO.MemoryStream(,[Convert]::FromBase64String(" + Quote(base64.StdEncoding.EncodeToString(b.Bytes())) + ")); $z=New-Object IO.Compression.GzipStream($m,[IO.Compression.CompressionMode]::Decompress); & ([ScriptBlock]::Create((New-Object IO.StreamReader($z)).ReadToEnd()))"
	}
	script = systemModules + script
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

func isASCIILetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func ValidHome(home string) bool {
	if len(home) < 3 || len(home) > 240 || home[1] != ':' || home[2] != '/' || !isASCIILetter(home[0]) {
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
func (l Layout) baseVars() string {
	return systemModules + "$ErrorActionPreference='Stop'\n$ProgressPreference='SilentlyContinue'\n" +
		"$root=" + Quote(l.Root()) + "\n$base=" + Quote(l.Base()) + "\n$name=" + Quote(l.Name()) + "\n"
}
func (l Layout) vars() string {
	return l.baseVars() + "$stage=" + Quote(l.Stage()) + "\n$task=" + Quote(l.Name()+"-Deploy-"+l.Attempt) + "\n"
}
func (l Layout) Prepare() string { return l.vars() + common + prepare }

// FetchArtifact is a file payload; it can exceed cmd.exe's command-line limit.
// RunFetch verifies the protected uploaded file before executing it.
func (l Layout) FetchArtifact(config, digest string, size int64, mode string, seconds int) string {
	return l.vars() + "$fetchConfig=" + Quote(config) + "\n$digest=" + Quote(digest) + "\n$mode=" + Quote(mode) + fmt.Sprintf("\n$size=%dL\n$seconds=%d\n", size, seconds) + common + fetch
}
func (l Layout) FetchFile(config, digest string, size int64, mode string, seconds int) []byte {
	return []byte("\xef\xbb\xbf" + l.FetchArtifact(config, digest, size, mode, seconds))
}
func RunFetch(script, digest string) string {
	return systemModules + "$ErrorActionPreference='Stop'\n" + common + "\n$script=" + Quote(script) + "; Assert-Path $script; if ((Get-FileHash -Algorithm SHA256 -LiteralPath $script).Hash -ne " + Quote(digest) + ") { throw 'Download script hash mismatch' }; & $script; exit $LASTEXITCODE"
}
func (l Layout) Activate(binaryHash, configHash string) string {
	return l.vars() + "$binaryHash=" + Quote(binaryHash) + "\n$configHash=" + Quote(configHash) + "\n" + common + activate
}

// Windows PowerShell 5.1 needs a BOM to read non-ASCII installation paths in a
// script file as UTF-8. Commands sent through SSH are already UTF-16 encoded.
func (l Layout) ActivationFile(binaryHash, configHash string) []byte {
	return []byte("\xef\xbb\xbf" + l.Activate(binaryHash, configHash))
}
func (l Layout) Launch(scriptHash string) string {
	return l.vars() + "$scriptHash=" + Quote(scriptHash) + fmt.Sprintf("\n$activationSeconds=%d\n", ActivationTaskSeconds) + launch
}
func (l Layout) Remove() string { return l.baseVars() + "$lockSeconds=200\n" + common + remove }

// RemovalFile is uploaded into the protected root before its hash is checked
// by RemoveDetached. Keeping the payload out of the SSH command also bounds
// command length when retry/receipt handling grows.
func (l Layout) RemovalPath() string { return l.Root() + "/" + l.Name() + ".remove.ps1" }
func (l Layout) RemovalFile() []byte {
	return []byte("\xef\xbb\xbf" + l.baseVars() + common + "function Invoke-Removal([int]$lockSeconds) {\n" + remove + "\n}\n" + removalResult)
}
func (l Layout) PrepareRemoval() string {
	return l.baseVars() + common + `# Protect removal payload before upload.
Assert-Admin
Protect-Directory $root
Assert-Path (Join-Path $root ($name+'.remove.ps1'))
Assert-Path (Join-Path $root ($name+'.remove.json'))
Assert-Path (Join-Path $root ($name+'.remove.json.tmp'))
`
}

// RemoveDetached lets a committed Hub deletion finish cleanup even if an
// activation still holds the lock or the SSH/request cleanup budget expires.
// No server credential is needed once the local SYSTEM task has been scheduled.
func (l Layout) RemoveDetached() string {
	return l.baseVars() + fmt.Sprintf("$scriptHash='%x'\n", sha256.Sum256(l.RemovalFile())) + `$script=Join-Path $root ($name+'.remove.ps1')
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $script).Hash -ne $scriptHash) { throw 'Removal script hash mismatch' }
$receipt=Join-Path $root ($name+'.remove.json')
Set-Content -LiteralPath $receipt -Value '{"status":"pending"}' -Encoding UTF8
$task=$name+'-Remove'
$exe=Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$action=New-ScheduledTaskAction -Execute $exe -Argument ('-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$script+'"')
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 20) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
$null=Register-ScheduledTask -TaskName $task -Action $action -Principal $principal -Settings $settings -Force
Start-ScheduledTask -TaskName $task
`
}

func (l Layout) WaitRemoval(seconds int) string {
	return l.baseVars() + fmt.Sprintf(`for ($i=0; $i -le %d; $i++) {
  $receipt=Join-Path $root ($name+'.remove.json')
  if (Test-Path -LiteralPath $receipt) {
    $r=Get-Content -LiteralPath $receipt -Raw | ConvertFrom-Json
    if ($r.status -eq 'succeeded') { [Console]::WriteLine('TALUS_REMOVE_OK'); exit 0 }
    if ($r.status -eq 'failed') { [Console]::WriteLine('TALUS_REMOVE_FAILED'); exit 0 }
  } elseif (!(Test-Path -LiteralPath $base) -and !(Get-Service -Name $name -ErrorAction SilentlyContinue)) {
    [Console]::WriteLine('TALUS_REMOVE_OK'); exit 0
  }
  if ($i -lt %d) { Start-Sleep -Seconds 1 }
}
[Console]::WriteLine('TALUS_REMOVE_PENDING')
`, seconds, seconds)
}
func (l Layout) Wait(attempts int) string {
	return l.vars() + fmt.Sprintf(`for ($i=0; $i -lt %d; $i++) {
  $result=Join-Path $stage 'result.json'
  if (Test-Path -LiteralPath $result) {
    $r=Get-Content -LiteralPath $result -Raw | ConvertFrom-Json
    if ($r.ok) { [Console]::WriteLine('TALUS_READY'); exit 0 }
    [Console]::Error.WriteLine($r.error)
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
