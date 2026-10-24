$script=Join-Path $stage 'activate.ps1'
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $script).Hash -ne $scriptHash) { throw 'Activation script hash mismatch' }
$exe=Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$action=New-ScheduledTaskAction -Execute $exe -Argument ('-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$script+'"')
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Seconds $activationSeconds) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
$null=Register-ScheduledTask -TaskName $task -Action $action -Principal $principal -Settings $settings -Force
Start-ScheduledTask -TaskName $task
