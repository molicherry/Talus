Assert-Admin
Protect-Directory $root
$lock=Lock-Install
try {
  Protect-Directory $base
  Protect-Directory (Join-Path $base 'cache')
  Protect-Directory (Join-Path $base 'staging')
  # Incomplete uploads and finished receipts cannot grow without bound. The
  # cutoff exceeds the activation task's hard limit; never remove a live stage.
  Get-ChildItem -LiteralPath (Join-Path $base 'staging') -Directory -Force | Where-Object { $_.LastWriteTime -lt (Get-Date).AddHours(-1) } | ForEach-Object {
    Assert-Path $_.FullName
    $links=Get-ChildItem -LiteralPath $_.FullName -Recurse -Force | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint }
    if ($links) { throw 'Reparse point in staging directory' }
    Remove-Item -LiteralPath $_.FullName -Recurse -Force
  }
  Protect-Directory $stage
  Protect-Directory (Join-Path $base 'outbox')
  # Abort before uploads if the detached activation mechanism is unavailable.
  if ((Get-Service Schedule).Status -ne 'Running') { throw 'Task Scheduler unavailable' }
} finally { $lock.Dispose() }
