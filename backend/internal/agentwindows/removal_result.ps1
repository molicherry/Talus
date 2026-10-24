$receipt=Join-Path $root ($name+'.remove.json')
Assert-Path $receipt
Assert-Path ($receipt+'.tmp')
$ok=$false
# RestartOnFailure is unreliable for tasks launched on demand without a trigger.
# Keep retries in the detached SYSTEM process, bounded by count and task runtime.
for ($attempt=1; $attempt -le 4; $attempt++) {
  try {
    Invoke-Removal
    $ok=$true
    $result=@{status='succeeded';attempt=$attempt;updatedAt=[DateTime]::UtcNow.ToString('o')}
  } catch {
    $result=@{status='failed';attempt=$attempt;error=$_.Exception.Message;updatedAt=[DateTime]::UtcNow.ToString('o')}
  }
  $result | ConvertTo-Json -Compress | Set-Content -LiteralPath ($receipt+'.tmp') -Encoding UTF8
  Move-Item -LiteralPath ($receipt+'.tmp') -Destination $receipt -Force
  if ($ok) { break }
  try { & eventcreate.exe /L APPLICATION /T WARNING /ID 100 /SO Talus-Agent /D ("Removal failed: "+$name+". See "+$receipt) 2>$null | Out-Null } catch {}
  if ($attempt -lt 4) { Start-Sleep -Seconds 60 }
}
if (!$ok) { exit 1 }
# Failed tasks retain their script and receipt for manual retries.
Unregister-ScheduledTask -TaskName ($name+'-Remove') -Confirm:$false -ErrorAction SilentlyContinue
Remove-Item -LiteralPath (Join-Path $root ($name+'.remove.ps1')) -Force -ErrorAction SilentlyContinue
