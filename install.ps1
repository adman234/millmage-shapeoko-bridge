# Installs MillMage Shapeoko Bridge on Windows and starts it with the computer.
# Run from an Administrator PowerShell window:
#   irm https://raw.githubusercontent.com/adman234/millmage-shapeoko-bridge/main/install.ps1 | iex
# Running it again updates the program and keeps your settings.

$ErrorActionPreference = 'Stop'

$repo     = 'adman234/millmage-shapeoko-bridge'
$taskName = 'MillMage Shapeoko Bridge'
$appDir   = Join-Path $env:ProgramFiles 'MillMageBridge'
$dataDir  = Join-Path $env:ProgramData 'MillMageBridge'
$exe      = Join-Path $appDir 'millmage-bridge.exe'
$conf     = Join-Path $appDir 'bridge.conf'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$isAdmin  = (New-Object Security.Principal.WindowsPrincipal $identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host 'Please run this from an Administrator PowerShell window (right-click PowerShell, Run as administrator).' -ForegroundColor Red
    return
}

$arch = 'amd64'
if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { $arch = 'arm64' }
$url = "https://github.com/$repo/releases/latest/download/millmage-bridge-windows-$arch.exe"

New-Item -ItemType Directory -Force -Path $appDir, $dataDir | Out-Null

if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
}
Get-Process -Name 'millmage-bridge' -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1

Write-Host "Downloading $url"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Invoke-WebRequest -Uri $url -OutFile $exe -UseBasicParsing

if (-not (Test-Path $conf)) {
    $lines = @(
        '# MillMage Shapeoko Bridge settings. After changing them, restart the',
        '# computer, or end and run the "MillMage Shapeoko Bridge" task in Task Scheduler.',
        '# All options: https://github.com/adman234/millmage-shapeoko-bridge#settings',
        'listen = :23',
        'http = :8080',
        'backend = carbide',
        'carbide-addr = 127.0.0.1:6280',
        "jobs-dir = $dataDir\jobs",
        "log-file = $dataDir\bridge.log",
        'require-end = true',
        'travel = 838,444,100'
    )
    [IO.File]::WriteAllLines($conf, $lines)
}

foreach ($rule in @(@{ Name = 'MillMage Bridge (MillMage)'; Port = 23 }, @{ Name = 'MillMage Bridge (status page)'; Port = 8080 })) {
    Get-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Action Allow -Protocol TCP -LocalPort $rule.Port -Program $exe -Profile Any | Out-Null
}

$action    = New-ScheduledTaskAction -Execute $exe -WorkingDirectory $appDir
$trigger   = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $taskName
Start-Sleep -Seconds 2

$ips = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
    Select-Object -ExpandProperty IPAddress

Write-Host ''
Write-Host 'MillMage Shapeoko Bridge is installed and running.' -ForegroundColor Green
Write-Host "  Version:      $(& $exe version)"
Write-Host "  Settings:     $conf"
Write-Host "  Jobs and log: $dataDir"
Write-Host ''
Write-Host 'Next steps:'
Write-Host '  1. In Carbide Motion on this computer, open Settings and switch on Allow Remote Access.'
foreach ($ip in $ips) {
    Write-Host "  2. In MillMage, add a GRBL device with a network (TCP) connection to $ip, port 23."
    Write-Host "  3. Status page: http://${ip}:8080"
}
