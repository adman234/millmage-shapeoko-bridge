# Removes MillMage Shapeoko Bridge from Windows. Run from an Administrator
# PowerShell window. Saved jobs and the log in ProgramData are left in place.

$ErrorActionPreference = 'Stop'
$taskName = 'MillMage Shapeoko Bridge'
$appDir   = Join-Path $env:ProgramFiles 'MillMageBridge'

if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}
Get-Process -Name 'millmage-bridge' -ErrorAction SilentlyContinue | Stop-Process -Force
Get-NetFirewallRule -DisplayName 'MillMage Bridge (*' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
Start-Sleep -Seconds 1
if (Test-Path $appDir) { Remove-Item -Recurse -Force $appDir }

Write-Host 'MillMage Shapeoko Bridge has been removed.'
Write-Host "Saved jobs and the log remain in $(Join-Path $env:ProgramData 'MillMageBridge')."
