Assert-Admin
Assert-Path $root
if (!(Test-Path -LiteralPath $root)) {
  if (Get-Service -Name $name -ErrorAction SilentlyContinue) { throw 'Service exists without installation root' }
  exit 0
}
Protect-Directory $root
$lock=Lock-Install 200
try {
  Assert-Path $base
  # Pending tasks cannot start another activation after removal. A running
  # activation holds this same lock through its rollback.
  Get-ScheduledTask -TaskName ($name+'-Deploy-*') -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false
  Remove-AgentService
  if (Test-Path -LiteralPath $base) {
    $links=Get-ChildItem -LiteralPath $base -Recurse -Force | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint }
    if ($links) { throw 'Reparse point in installation state' }
    Remove-Item -LiteralPath $base -Recurse -Force
  }
} finally { $lock.Dispose() }
