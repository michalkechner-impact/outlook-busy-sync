# Register a Windows Scheduled Task that runs `outlook-busy-sync sync`
# every 15 minutes while the user is logged in. Run once, in a regular
# (non-elevated) PowerShell prompt:
#
#     powershell -ExecutionPolicy Bypass -File .\Register-Task.ps1
#
# The task runs as the current user with the user's normal privileges;
# no admin rights required.

$ErrorActionPreference = 'Stop'

$taskName = 'outlook-busy-sync'

# Resolve the binary: prefer a Scoop/manual install in the user profile,
# fall back to whatever's on PATH.
$exe = (Get-Command outlook-busy-sync -ErrorAction SilentlyContinue).Source
if (-not $exe) {
    throw "outlook-busy-sync not found on PATH. Install it first (scoop install outlook-busy-sync, or unzip a release to a folder on PATH)."
}

$logDir = Join-Path $env:LOCALAPPDATA 'outlook-busy-sync\logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$logFile = Join-Path $logDir 'sync.log'

# `cmd /c ... >> log 2>&1` lets us capture stdout+stderr without wrapping
# the whole thing in PowerShell, which would add cold-start latency on
# every run.
$action = New-ScheduledTaskAction `
    -Execute 'cmd.exe' `
    -Argument "/c `"$exe`" sync >> `"$logFile`" 2>&1"

$trigger = New-ScheduledTaskTrigger `
    -Once `
    -At (Get-Date).AddMinutes(2) `
    -RepetitionInterval (New-TimeSpan -Minutes 15)

$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -MultipleInstances IgnoreNew `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 10)

$principal = New-ScheduledTaskPrincipal `
    -UserId $env:USERNAME `
    -LogonType Interactive `
    -RunLevel Limited

if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

Register-ScheduledTask `
    -TaskName $taskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Description 'Mirror Microsoft 365 busy blocks between two calendars.' | Out-Null

Write-Host "Registered scheduled task '$taskName'."
Write-Host "Logs: $logFile"
Write-Host "Inspect: Get-ScheduledTask -TaskName $taskName | Get-ScheduledTaskInfo"
Write-Host "Uninstall: Unregister-ScheduledTask -TaskName $taskName -Confirm:`$false"
