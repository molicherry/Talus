function Assert-Admin {
  $p=[Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
  if (!$p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator required' }
}
function Assert-Path($p) {
  $p=[IO.Path]::GetFullPath($p)
  while ($p) {
    if (Test-Path -LiteralPath $p) {
      if ((Get-Item -Force -LiteralPath $p).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse point in installation path' }
    }
    $p=[IO.Path]::GetDirectoryName($p)
  }
}
function Protect-Directory($p) {
  Assert-Path $p
  if (Test-Path -LiteralPath $p) {
    $owner=(Get-Acl -LiteralPath $p).GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -ne 'S-1-5-18' -and $owner -ne 'S-1-5-32-544') { throw 'Untrusted installation owner' }
  } else { $null=New-Item -ItemType Directory -Path $p -Force }
  $acl=New-Object Security.AccessControl.DirectorySecurity
  $acl.SetAccessRuleProtection($true,$false)
  foreach ($sid in @('S-1-5-18','S-1-5-32-544')) {
    $id=New-Object Security.Principal.SecurityIdentifier($sid)
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($id,'FullControl','ContainerInherit,ObjectInherit','None','Allow')))
  }
  $acl.SetOwner((New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))
  Set-Acl -LiteralPath $p -AclObject $acl
}
function Lock-Install([int]$seconds=10) {
  $p=Join-Path $root ($name+'.lock')
  Assert-Path $p
  for ($i=0; $i -lt ($seconds*10); $i++) {
    try { return [IO.File]::Open($p,[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None) }
    catch [IO.IOException] { Start-Sleep -Milliseconds 100 }
  }
  throw 'Another deployment owns the installation'
}
function Stop-Agent {
  $s=Get-Service -Name $name -ErrorAction SilentlyContinue
  if ($s -and $s.Status -ne 'Stopped') {
    $agentPid=(Get-CimInstance Win32_Service -Filter ("Name='"+$name+"'")).ProcessId
    $process=Get-Process -Id $agentPid -ErrorAction SilentlyContinue
    $s.Stop()
    $s.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(15))
    if ($process -and !$process.WaitForExit(15000)) { throw 'Agent process has not exited' }
  }
}
function Remove-AgentService {
  Stop-Agent
  if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
    & sc.exe delete $name | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot delete agent service' }
    for ($i=0; $i -lt 50; $i++) {
      if (!(Get-Service -Name $name -ErrorAction SilentlyContinue)) { return }
      Start-Sleep -Milliseconds 100
    }
    throw 'Agent service deletion is pending'
  }
}
