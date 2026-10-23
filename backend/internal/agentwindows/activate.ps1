$lock=$null
$changed=$false
$previous=$false
$ok=$false
$binary=Join-Path $base 'agent.exe'
$config=Join-Path $base 'config.json'
$nextBinary=Join-Path $stage 'agent.exe'
$nextConfig=Join-Path $stage 'config.json'
try {
  Assert-Admin
  Assert-Path $stage
  $lock=Lock-Install
  # A deletion may have won the lock while this detached task was starting.
  if (!(Test-Path -LiteralPath $stage -PathType Container)) { throw 'Installation removed' }
  foreach ($p in @($binary,$config,$nextBinary,$nextConfig,(Join-Path $base 'outbox'))) { Assert-Path $p }
  if ((Get-FileHash -Algorithm SHA256 -LiteralPath $nextBinary).Hash -ne $binaryHash) { throw 'Binary hash mismatch' }
  if ((Get-FileHash -Algorithm SHA256 -LiteralPath $nextConfig).Hash -ne $configHash) { throw 'Configuration hash mismatch' }
  $oldService=Get-CimInstance Win32_Service -Filter ("Name='"+$name+"'")
  if ($oldService) {
    # Never take over a service with this name that points outside our layout.
    $oldCommand=$oldService.PathName.Replace('\','/')
    if (!$oldCommand.StartsWith('"'+$binary.Replace('\','/')+'"',[StringComparison]::OrdinalIgnoreCase)) { throw 'Unexpected service executable' }
    if (!(Test-Path -LiteralPath $binary) -or !(Test-Path -LiteralPath $config)) { throw 'Incomplete previous installation' }
    Copy-Item -LiteralPath $binary -Destination ($binary+'.previous') -Force
    Copy-Item -LiteralPath $config -Destination ($config+'.previous') -Force
    $previous=$true
  }
  Stop-Agent
  $changed=$true
  Move-Item -LiteralPath $nextBinary -Destination $binary -Force
  Move-Item -LiteralPath $nextConfig -Destination $config -Force
  $health=Join-Path $base 'outbox/health.json'
  if (Test-Path -LiteralPath $health) { Remove-Item -LiteralPath $health -Force }
  $command='"'+$binary+'" --daemon --service-name '+$name+' --config "'+$config+'"'
  if (!$oldService) {
    $null=New-Service -Name $name -BinaryPathName $command -DisplayName $name -StartupType Automatic -Description 'Talus monitoring agent'
  } else { Set-Service -Name $name -StartupType Automatic }
  & sc.exe failure $name reset= 86400 actions= restart/3000/restart/10000/restart/30000 | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'Cannot configure service recovery' }
  & sc.exe failureflag $name 1 | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'Cannot configure service recovery flag' }
  Start-Service -Name $name
  for ($i=0; $i -lt 25; $i++) {
    # Windows PowerShell turns redirected native stderr into ErrorRecords.
    # A failed readiness check is expected while the service is connecting.
    $ErrorActionPreference='Continue'
    & $binary --health --config $config 2>$null
    $healthExit=$LASTEXITCODE
    $ErrorActionPreference='Stop'
    if ($healthExit -eq 0) { $ok=$true; break }
    Start-Sleep -Seconds 1
  }
  if (!$ok) { throw 'New service did not authenticate with Hub' }
} catch {
  if ($changed) {
    try {
      Stop-Agent
      if ($previous) {
        Copy-Item -LiteralPath ($binary+'.previous') -Destination $binary -Force
        Copy-Item -LiteralPath ($config+'.previous') -Destination $config -Force
        $mode=@{Auto='Automatic';Manual='Manual';Disabled='Disabled'}[$oldService.StartMode]
        Set-Service -Name $name -StartupType $mode
        if ($oldService.State -eq 'Running') { Start-Service -Name $name }
      } else {
        Remove-AgentService
        foreach ($p in @($binary,$config)) { if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force } }
      }
    } catch { $ok=$false }
  }
} finally {
  if ($lock) {
    try {
      if (Test-Path -LiteralPath $stage) {
        foreach ($p in @($nextBinary,$nextConfig)) { if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force -ErrorAction SilentlyContinue } }
        $result=Join-Path $stage 'result.json'
        [IO.File]::WriteAllText(($result+'.tmp'),(@{ok=$ok}|ConvertTo-Json -Compress))
        Move-Item -LiteralPath ($result+'.tmp') -Destination $result -Force
      }
    } finally { $lock.Dispose() }
  }
  Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
}
if (!$ok) { exit 1 }
