<#
.SYNOPSIS
    Registers (or replaces) the "go-polipo" Task Scheduler entry and starts it.
    Mirrors the manual steps documented in docs/INSTALL.md / docs/INSTALL.ja.md section 6.
#>
param(
    [Parameter(Mandatory = $true)][string]$ExePath,
    [Parameter(Mandatory = $true)][string]$ConfigPath
)

$action    = New-ScheduledTaskAction -Execute $ExePath -Argument "-c `"$ConfigPath`"" -WorkingDirectory (Split-Path $ExePath)
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings  = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -MultipleInstances IgnoreNew
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited

Register-ScheduledTask -TaskName "go-polipo" -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
Start-ScheduledTask -TaskName "go-polipo"
