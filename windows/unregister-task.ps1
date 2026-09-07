<#
.SYNOPSIS
    Stops and removes the "go-polipo" Task Scheduler entry. Run by the
    uninstaller; safe to run even if the task was never registered.
#>

Stop-ScheduledTask -TaskName "go-polipo" -ErrorAction SilentlyContinue
Get-Process polipo -ErrorAction SilentlyContinue | Stop-Process -Force
Unregister-ScheduledTask -TaskName "go-polipo" -Confirm:$false -ErrorAction SilentlyContinue
