$receipt=Join-Path $root ($name+'.remove.json')
Assert-Path $receipt
Assert-Path ($receipt+'.tmp')
$ok=$false
try {
  Invoke-Removal
  $ok=$true
  $result=@{status='succeeded';updatedAt=[DateTime]::UtcNow.ToString('o')}
} catch {
  $result=@{status='failed';error=$_.Exception.Message;updatedAt=[DateTime]::UtcNow.ToString('o')}
}
$result | ConvertTo-Json -Compress | Set-Content -LiteralPath ($receipt+'.tmp') -Encoding UTF8
Move-Item -LiteralPath ($receipt+'.tmp') -Destination $receipt -Force
if (!$ok) {
  try { & eventcreate.exe /L APPLICATION /T WARNING /ID 100 /SO Talus-Agent /D ("Removal failed: "+$name+". See "+$receipt) 2>$null | Out-Null } catch {}
  exit 1
}
# Failed tasks retain their script and receipt for automatic or manual retries.
Unregister-ScheduledTask -TaskName ($name+'-Remove') -Confirm:$false -ErrorAction SilentlyContinue
Remove-Item -LiteralPath (Join-Path $root ($name+'.remove.ps1')) -Force -ErrorAction SilentlyContinue
