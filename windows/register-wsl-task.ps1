<#
.SYNOPSIS
    Registers (or replaces) the "go-polipo-wsl" Task Scheduler entry, which
    wakes WSL so that systemd starts go-polipo inside it, and runs it once.
    See docs/INSTALL.md / docs/INSTALL.ja.md section 6.

.DESCRIPTION
    The task runs `wsl.exe -d <Distro> -u <User> -- true` (through
    `conhost --headless`, so no console window appears) at logon, at
    workstation unlock, and on resume from sleep. A Startup-folder shortcut
    only fires at sign-in, and late; after a resume the WSL VM is often
    restarted too, which the shortcut never covers. Running it while WSL is
    already up is harmless: `true` just exits.

.EXAMPLE
    .\register-wsl-task.ps1 -User alice
    .\register-wsl-task.ps1 -Distro Debian -User bob
#>
param(
    [string]$Distro = "Ubuntu",
    [Parameter(Mandatory = $true)][string]$User
)

$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$wsl = Join-Path $env:WINDIR "System32\wsl.exe"
$conhost = Join-Path $env:WINDIR "System32\conhost.exe"
$resumeQuery = '<QueryList><Query Id="0" Path="System"><Select Path="System">' +
    "*[System[Provider[@Name='Microsoft-Windows-Power-Troubleshooter'] and EventID=1]]" +
    '</Select></Query></QueryList>'

$xml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Wake WSL ($Distro) so systemd starts go-polipo: at logon, at unlock, and on resume from sleep.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>$sid</UserId></LogonTrigger>
    <SessionStateChangeTrigger><Enabled>true</Enabled><StateChange>SessionUnlock</StateChange><UserId>$sid</UserId></SessionStateChangeTrigger>
    <EventTrigger><Enabled>true</Enabled><Subscription>$([System.Security.SecurityElement]::Escape($resumeQuery))</Subscription></EventTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author"><UserId>$sid</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT5M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>$conhost</Command>
      <Arguments>--headless $wsl -d $Distro -u $User -- true</Arguments>
    </Exec>
  </Actions>
</Task>
"@

Register-ScheduledTask -TaskName "go-polipo-wsl" -Xml $xml -Force | Out-Null
Start-ScheduledTask -TaskName "go-polipo-wsl"
