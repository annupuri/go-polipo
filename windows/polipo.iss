; Inno Setup script for go-polipo (Windows installer/uninstaller).
;
; Build locally (not distributed via CI): install Inno Setup
; (https://jrsoftware.org/isinfo.php), then from the repo root:
;
;   make build-windows WINGUI=1
;   iscc windows\polipo.iss
;
; The output installer is written to windows\dist\go-polipo-setup.exe.
; Keep AppVersion below in sync with the Version constant in version.go.

#define MyAppName "go-polipo"
#define MyAppVersion "2.0.0"
#define MyAppExeName "polipo.exe"

[Setup]
AppId={{6C6E1F0E-6E9C-4C0E-9E12-6E6B6F1D9C21}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=go-polipo
DefaultDirName={localappdata}\Programs\go-polipo
DisableDirPage=yes
DisableProgramGroupPage=yes
; Admin is required to register the scheduled task (see [Run] below), even
; though the app itself installs per-user. This is only correct when the
; account requesting elevation is the same one you're installing for - the
; compiler warns about exactly this; there is no fix, just don't elevate as
; a different admin account on a shared PC.
PrivilegesRequired=admin
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=dist
OutputBaseFilename=go-polipo-setup
Compression=lzma2
SolidCompression=yes
UninstallDisplayIcon={app}\{#MyAppExeName}

[Files]
Source: "..\bin\polipo.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\config.sample"; DestDir: "{app}"; Flags: ignoreversion
Source: "register-task.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "unregister-task.ps1"; DestDir: "{app}"; Flags: ignoreversion

[Dirs]
; Parent folder for logFile; go-polipo does not create it itself (it does
; create diskCacheRoot on first use). See docs/INSTALL.md section 8.
Name: "{localappdata}\go-polipo"

[Run]
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\register-task.ps1"" -ExePath ""{app}\{#MyAppExeName}"" -ConfigPath ""{code:GetConfigPath}"""; Flags: runhidden; StatusMsg: "Registering the go-polipo startup task..."

[UninstallRun]
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\unregister-task.ps1"""; Flags: runhidden; RunOnceId: "UnregisterGoPolipoTask"

[Code]
function GetConfigPath(Param: String): String;
begin
  Result := ExpandConstant('{%USERPROFILE}') + '\.polipo';
end;

// Re-running this installer over an existing install (to update the binary)
// would otherwise fail to overwrite a running, locked polipo.exe. Stop it
// first, same as the manual "Updating the binary" steps in docs/INSTALL.md.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  Exec('powershell.exe',
    '-NoProfile -ExecutionPolicy Bypass -Command "Stop-ScheduledTask -TaskName ''go-polipo'' -ErrorAction SilentlyContinue; Get-Process polipo -ErrorAction SilentlyContinue | Stop-Process -Force"',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ConfigPath: String;
begin
  if CurStep = ssPostInstall then
  begin
    ConfigPath := GetConfigPath('');
    if not FileExists(ConfigPath) then
      CopyFile(ExpandConstant('{app}\config.sample'), ConfigPath, False);
  end;
end;
