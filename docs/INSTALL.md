go-polipo (v2.0.0) Installation Instructions
=============================================

(日本語版は INSTALL.ja.md を参照してください / A Japanese version is available in INSTALL.ja.md)

This is a Go-based implementation of the Polipo caching web proxy.

## 1. Requirements

To build go-polipo, you need to have Go (version 1.27.4 or later) installed on your system.

## 2. Building and running go-polipo

You can build the binary using the provided Makefile:

    $ make build

By default, this writes the binary to `bin/polipo` (or `bin/polipo.exe` on Windows).

To install the binary to `/usr/local/bin` (on POSIX systems):

    $ sudo make install

To run the proxy:

    $ ./bin/polipo

By default, the proxy runs in the foreground and listens on http://127.0.0.1:8123/.

## 3. Configuring your web browser

Configure your web browser to use the proxy on `http://localhost:8123/`.
You can also set the proxy via environment variables:

    $ export http_proxy=http://127.0.0.1:8123
    $ export https_proxy=http://127.0.0.1:8123

### Windows: Microsoft Store and other Store apps

If Windows' system proxy points at `127.0.0.1:8123`, Microsoft Store (and
other packaged/UWP apps) may show an error, while working fine with the
proxy turned off. This is not a go-polipo problem - original Polipo is
affected the same way, as is go-polipo running natively or inside WSL.
These apps run in an AppContainer sandbox, which Windows by default forbids
from connecting to loopback addresses, so the requests never reach the
proxy (and nothing shows up in its log).

Exempt the app from loopback isolation, from an **elevated** prompt:

    CheckNetIsolation LoopbackExempt -a '-n=Microsoft.WindowsStore_8wekyb3d8bbwe'

Keep the quotes in PowerShell: unquoted, PowerShell splits the argument at
the `.` and CheckNetIsolation fails with "The parameter is incorrect". In
cmd.exe they are not needed. No restart is required - just reopen the app.
`CheckNetIsolation LoopbackExempt -s` lists the exemptions; `-d` instead of
`-a` removes one. For another Store app, find its package family name with
`Get-AppxPackage <name> | Select-Object PackageFamilyName`.

**go-polipo inside WSL in mirrored networking mode** (section 6): there,
127.0.0.1:8123 is served by go-polipo inside the WSL VM, not by a Windows
process, and the loopback exemption may not cover that path. This was seen
after upgrading to Windows 11 26H2 (build 26300): the exemption was still
listed, removing and re-adding it did not help, the Store failed with
"Microsoft Store couldn't complete the connection" (`0x80072EFD` in the
Microsoft-Windows-Store/Operational event log) and its process
(`WinStore.App`) never opened a connection to the proxy. Let the Store
bypass the proxy instead: in Settings > Network & internet > Proxy > Use a
proxy server > Edit, add to "Use the proxy server except for addresses that
start with the following entries":

    *.microsoft.com;*.s-microsoft.com;*.xboxservices.com;*.xboxlive.com

This covers every host the Store contacted over a week (`storeedge`,
`licensing.mp`, `displaycatalog`, `sfdataservice`, `login`, ... under
`microsoft.com`, plus `da.xboxservices.com` and `images-eds-ssl.xboxlive.com`).
Store traffic is HTTPS through CONNECT, which go-polipo never caches, so
nothing is lost by bypassing it; other applications will also reach these
domains directly. The only Store requests still failing afterwards were its
ad-tracking events (Microsoft Advertising's Universal Event Tracking, "UET"
in the event log), which are harmless: they only report ad impressions and
clicks back to advertisers. UET normally reports to `bat.bing.com`, so adding
`*.bing.com` to the list should let them through as well.

## 4. Configuring go-polipo

If you want to use an on-disk cache, make sure the cache directory exists and is writable:

    $ mkdir -p /var/cache/polipo/

Configuration files can be placed in standard locations:
- `~/.polipo`
- `/etc/polipo/config`

You can also specify a custom configuration file path using the `-c` flag:

    $ ./bin/polipo -c /path/to/config

`config.sample` lists the supported variables with their defaults. You can
also see the effective configuration with `./bin/polipo -v`, or open the local
configuration page at `http://127.0.0.1:8123/polipo/config` while the proxy
is running.

## 5. Running go-polipo on Windows: which way to choose

On Linux, macOS and other POSIX systems, sections 1-4 are all you need. On
Windows there are two ways to run go-polipo:

- **Inside WSL2 (recommended, section 6).** The native Linux build runs as a
  systemd service inside WSL2, and Windows applications reach it at
  `http://127.0.0.1:8123/` as usual. Smart App Control does not get in the
  way, because the only Windows program involved is the Microsoft-signed
  `wsl.exe`.
- **As a native Windows program (sections 7-9).** This works only where
  Smart App Control is Off. go-polipo's `polipo.exe` is not code-signed, and
  Windows 11 with Smart App Control On blocks it (see section 8). Smart App
  Control is Off on PCs upgraded from Windows 10; on a clean install of
  Windows 11 it starts in evaluation mode and may turn itself On. Check it in
  Windows Security > App & browser control > Smart App Control settings. If
  you are not sure, use WSL.

## 6. Running go-polipo inside WSL (recommended)

This is the recommended way to run go-polipo on Windows (see section 5).
The native Linux build runs inside WSL2, so Windows never has to run an
unsigned `polipo.exe`, and Smart App Control does not get in the way. WSL2
forwards `127.0.0.1` between Windows and the Linux VM by default, so Windows
applications point at `http://127.0.0.1:8123/` as usual.

Build the native Linux binary from inside WSL:

    $ make build

(WSL's default `make build` already produces the Linux binary - no
`GOOS`/`build-windows` needed, see section 7.)

Config, cache and log live inside the WSL filesystem, e.g. `~/.polipo`:

    proxyAddress = 127.0.0.1
    proxyPort = 8123
    diskCacheRoot = /home/<you>/.cache/go-polipo/cache
    logFile = /home/<you>/.cache/go-polipo/polipo.log

go-polipo creates `diskCacheRoot` itself on first use, but not `logFile`'s
parent directory:

    $ mkdir -p ~/.cache/go-polipo

### Run as a systemd service

Most current WSL2 distros have systemd enabled by default; check with:

    $ ps -p 1 -o comm=

If it prints `systemd`, copy the sample unit file `systemd/go-polipo.service`
and replace `<you>` and `/path/to/go-polipo` in it:

    $ cp systemd/go-polipo.service ~/go-polipo.service
    $ sed -i "s|<you>|$USER|g; s|/path/to/go-polipo|$PWD|" ~/go-polipo.service

Its `RestartSec=5` and `StartLimitIntervalSec=0` keep systemd retrying while
port 8123 is still held by something else - which happens for up to two
minutes after a WSL restart in mirrored networking mode (see "IPv6 and
mirrored networking" below). Without them, systemd's defaults (retry after
100 ms, give up after 5 starts within 10 s) stop the service for good within
a second.

Install and enable it:

    $ sudo cp ~/go-polipo.service /etc/systemd/system/go-polipo.service
    $ sudo systemctl daemon-reload
    $ sudo systemctl enable --now go-polipo
    $ sudo systemctl status go-polipo --no-pager

### Keep the WSL2 VM (and the service) alive

By default WSL2 shuts its background VM down shortly after the last WSL
terminal/window closes, taking systemd and go-polipo down with it. Add this
to `%UserProfile%\.wslconfig` on the **Windows** side to disable that:

    [general]
    instanceIdleTimeout=-1

    [wsl2]
    vmIdleTimeout=-1

`vmIdleTimeout` keeps the VM up; `instanceIdleTimeout` keeps the distro
itself from being shut down once no WSL session is attached. In the default
NAT mode the distro happens to stay up without it (WSL's localhost relay
keeps it busy), but in mirrored mode it is required: without it the distro,
and go-polipo with it, powers off about 15 seconds after the logon task
below has run.

### Start WSL automatically at Windows logon (and after sleep)

The WSL2 VM does not start on its own at Windows logon, and in practice it
is often restarted when Windows resumes from sleep as well. A Task Scheduler
task that runs the Microsoft-signed (and thus Smart-App-Control-exempt)
`wsl.exe` at logon, at workstation unlock and on resume from sleep wakes it
each time, which lets systemd start go-polipo in turn. Running it while WSL
is already up is harmless. `windows/register-wsl-task.ps1` registers this
task as `go-polipo-wsl` for the current Windows user and runs it once. In
**Windows PowerShell** (no elevation is normally needed; if registering
fails with "Access is denied", run it from an elevated prompt):

    powershell -NoProfile -ExecutionPolicy Bypass -File \\wsl.localhost\<YourDistroName>\path\to\go-polipo\windows\register-wsl-task.ps1 -Distro <YourDistroName> -User <you>
    Get-ScheduledTaskInfo -TaskName go-polipo-wsl | Select-Object LastRunTime, LastTaskResult   # 0 = success

The task runs `wsl.exe -d <YourDistroName> -u <you> -- true` through
`conhost --headless`, so no console window flashes up. To remove it:

    Unregister-ScheduledTask -TaskName go-polipo-wsl -Confirm:$false

A Startup-folder shortcut running the same `wsl.exe` command also works, but
only at sign-in, and late: Windows runs Startup items only after the desktop
has loaded, so go-polipo came up 40-60 seconds after logon (about 10 seconds
with the task), and applications opened before then could not reach the proxy. It does nothing after a resume
from sleep. If you created one earlier, remove it once the task is in place.

Verify after a Windows restart, and after a resume from sleep:

    Test-NetConnection 127.0.0.1 -Port 8123

### IPv6 and mirrored networking

In WSL's default NAT networking mode the Linux VM has no IPv6
connectivity, even when Windows has. go-polipo then cannot reach hosts that
publish only an IPv6 (AAAA) address; the log shows e.g.

    CONNECT failed: dial example.com:443: dial tcp [2606:...]:443: connect: network is unreachable

while the same host works from Windows with the proxy off. Mirrored mode
gives WSL the same IPv4 and IPv6 addresses as Windows. It needs Windows 11
22H2 or later. Add it to `%UserProfile%\.wslconfig`, together with the
`instanceIdleTimeout` setting above:

    [general]
    instanceIdleTimeout=-1

    [wsl2]
    vmIdleTimeout=-1
    networkingMode=mirrored

Things that change in mirrored mode:

- **Windows and WSL share one port space.** A Windows process listening on
  127.0.0.1:8123 keeps go-polipo from starting (`bind: address already in
  use` in the log). Make sure the Windows task or shortcut from section 8 is
  disabled.
- **Ports in TIME_WAIT on Windows count as in use.** In NAT mode, WSL's
  localhost relay (`wslrelay.exe`) accepts the Windows-side connections to
  port 8123; after `wsl --shutdown` those linger in TIME_WAIT for up to two
  minutes (the Windows default). Right after switching from NAT to mirrored
  mode, or after any later WSL restart, go-polipo may therefore fail to bind
  for up to two minutes. `systemctl is-active go-polipo` shows `activating`
  while systemd keeps retrying, then `active`, provided the unit file has
  the `RestartSec`/`StartLimitIntervalSec` settings above. This does not
  happen at Windows startup.
- **Microsoft Store may need to bypass the proxy**, as the loopback
  exemption from section 3 may no longer be enough - see the end of that
  section.
- **IPv6 is preferred when a host has both address families.** go-polipo
  (Go's dialer) tries both and uses whichever connects first, so upstream
  addresses in the log may now be IPv6.

Switching, in **Windows PowerShell**:

    wsl --shutdown
    # edit %UserProfile%\.wslconfig as above, then:
    wsl -e true
    Start-Sleep 150                               # allow for the TIME_WAIT delay
    wsl -l -v                                     # STATE: Running
    wsl -e systemctl is-active go-polipo          # active
    Test-NetConnection 127.0.0.1 -Port 8123       # TcpTestSucceeded : True

From inside WSL, `ip -6 route` should now list a `default` route. To go
back, remove the `networkingMode` line and run `wsl --shutdown` again.

### Updating the binary

Unlike Windows, WSL/Linux does not lock a running executable, so no
stop-before-copy dance is needed:

    $ git pull
    $ make build
    $ sudo systemctl restart go-polipo

If you had run go-polipo natively on Windows before, with the Task
Scheduler task or Startup-folder shortcut from section 8, disable or remove
it so the two don't fight over port 8123:

    Disable-ScheduledTask -TaskName "go-polipo"   # run as Administrator

## 7. Building on Windows

This section and the next two are for running go-polipo as a native Windows
program. See section 5 first: on most Windows 11 machines, running it inside
WSL (section 6) is the way that works.

Go builds Windows binaries out of the box. You do not need Cygwin, MinGW, or
any third-party POSIX emulation layer to compile or run go-polipo on Windows.

Native build (PowerShell or Command Prompt, with Go installed):

    > make build

or, without `make`:

    > go build -o bin\polipo.exe .

Cross-compiling for Windows from Unix/Linux/macOS/WSL:

    $ make build-windows                  # -> bin/polipo.exe (amd64)
    $ make build-windows WINARCH=arm64    # -> bin/polipo.exe (arm64)
    $ make build-windows WINGUI=1         # no console window on launch

or without `make`:

    $ GOOS=windows GOARCH=amd64 go build -o bin/polipo.exe .

`make build` on WSL produces the native Linux `bin/polipo`; use
`build-windows` (or set `GOOS=windows`) to get `bin/polipo.exe`.

Paths: unlike the C version, go-polipo follows the host's path conventions
automatically. Make sure `diskCacheRoot` and any other paths in your
configuration file are valid Windows paths, e.g. `C:\var\cache\polipo\`.

Running at startup: build with `WINGUI=1` so no console window appears, set
`logFile` in the configuration (a `-H windowsgui` build has no console for
its log output), then register a Task Scheduler task with an "At log on"
trigger pointing at `polipo.exe -c <config>`. See section 8 below for the
full, tested procedure.

## 8. Running go-polipo at Windows startup

**Not currently recommended if Smart App Control is On.** Windows 11's
Smart App Control, once it locks into the "On" state (the "Evaluation"
toggle becomes unselectable), blocks execution of unsigned, self-built
binaries like `polipo.exe` outright ("This app has been blocked by your
system administrator" / an application-control-policy error), with no
supported way back to "Off" short of a clean Windows reinstall, and no
self-signing workaround. If that's your situation, use section 6 (running
go-polipo inside WSL) instead. This section remains for machines
where Smart App Control is Off or still evaluating, or once the binary has
proper code signing.

Recommended layout (per-user; no admin rights needed for the files
themselves):

    %LOCALAPPDATA%\Programs\go-polipo\polipo.exe    binary
    %USERPROFILE%\.polipo                           config (auto-discovered)
    %LOCALAPPDATA%\go-polipo\cache\                 diskCacheRoot
    %LOCALAPPDATA%\go-polipo\polipo.log             logFile

go-polipo creates `diskCacheRoot` itself on first use, but it does **not**
create `logFile`'s parent directory, so create it once:

    > mkdir "%LOCALAPPDATA%\go-polipo"

Example `%USERPROFILE%\.polipo`:

    proxyAddress = 127.0.0.1
    proxyPort = 8123
    diskCacheRoot = C:/Users/<you>/AppData/Local/go-polipo/cache
    logFile = C:/Users/<you>/AppData/Local/go-polipo/polipo.log

Build a console-free binary (see section 7) and copy `bin\polipo.exe` to
`%LOCALAPPDATA%\Programs\go-polipo\polipo.exe`:

    $ make build-windows WINGUI=1

### Register a scheduled task (recommended)

Run this in an **elevated** PowerShell — registering the task needs admin
rights, but the task itself still runs under your normal, unprivileged
account (`-RunLevel Limited`):

    $exe = "$env:LOCALAPPDATA\Programs\go-polipo\polipo.exe"
    $cfg = "$env:USERPROFILE\.polipo"

    $action    = New-ScheduledTaskAction -Execute $exe -Argument "-c `"$cfg`"" -WorkingDirectory (Split-Path $exe)
    $trigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings  = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -MultipleInstances IgnoreNew
    $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited

    Register-ScheduledTask -TaskName "go-polipo" -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force

If `Register-ScheduledTask` still fails with "Access is denied" after
elevating, some managed/corporate machines block Task Scheduler entirely via
policy; use the Startup-folder fallback below instead.

### Verify it started

    Start-ScheduledTask -TaskName "go-polipo"
    Get-ScheduledTask -TaskName "go-polipo" | Get-ScheduledTaskInfo
    Test-NetConnection 127.0.0.1 -Port 8123
    Get-Content "$env:LOCALAPPDATA\go-polipo\polipo.log" -Tail 5

`LastTaskResult` is a **Task Scheduler** status code, not go-polipo's exit
code: `267009` (`0x41301`, "task is currently running") means it is up and
healthy; `0` means it ran and exited cleanly (e.g. after `-h`/`-v`/`-x`). Any
other small integer (`1`, `2`, ...) is go-polipo's own exit code — check the
log file for the reason. `TcpTestSucceeded : True` confirms the proxy is
listening, and the log should show a "starting on http://..." line.

### Fallback: Startup-folder shortcut (no special permissions needed)

    $exe = "$env:LOCALAPPDATA\Programs\go-polipo\polipo.exe"
    $cfg = "$env:USERPROFILE\.polipo"
    $startup = [Environment]::GetFolderPath('Startup')
    $ws = New-Object -ComObject WScript.Shell
    $sc = $ws.CreateShortcut("$startup\go-polipo.lnk")
    $sc.TargetPath       = $exe
    $sc.Arguments        = "-c `"$cfg`""
    $sc.WorkingDirectory = Split-Path $exe
    $sc.Save()

This starts go-polipo at logon with no elevation and (with a `WINGUI=1`
build) no console window, but without Task Scheduler's automatic restart on
crash. Remove it with `Remove-Item "$startup\go-polipo.lnk"`.

### Updating the binary

Windows keeps a running executable locked, so stop go-polipo before
overwriting it:

    Stop-ScheduledTask -TaskName "go-polipo"
    Get-Process polipo -ErrorAction SilentlyContinue | Stop-Process -Force
    # copy the newly built bin\polipo.exe into place, then:
    Start-ScheduledTask -TaskName "go-polipo"

If a console window pops up when the task starts, the binary you just built
and copied in is missing `WINGUI=1` - it's a regular console-subsystem
executable, not the console-less one. Closing that window sends it a close
signal that terminates go-polipo along with it, which looks like "the proxy
randomly stops working" with no error in the log. Rebuild with `WINGUI=1`
(section 7) and repeat the steps above.

## 9. Windows installer (optional)

Section 8 and its "Updating the binary" above can be done by hand every time, or
packaged into a one-time installer/uninstaller built with
[Inno Setup](https://jrsoftware.org/isinfo.php). This is not distributed as a
release asset - you build it yourself, locally, from `windows/polipo.iss`.

Install Inno Setup once, then from the repo root:

    $ make build-windows WINGUI=1
    $ iscc windows/polipo.iss

(`make installer` runs both steps.) This produces
`windows/dist/go-polipo-setup.exe`. Running it (admin privileges are required,
since registering the scheduled task needs them) does everything sections 4
and 8 describe by hand:

- deploys `polipo.exe` to `%LOCALAPPDATA%\Programs\go-polipo\`
- creates `%USERPROFILE%\.polipo` from `config.sample` if one doesn't already
  exist (an existing config is never overwritten)
- registers and starts the `go-polipo` scheduled task from section 8

Running the installer again (e.g. after rebuilding with a code change) stops
the running task/process first, then overwrites the binary - the same
`Stop-ScheduledTask` / `Stop-Process` sequence as manual updates, just
automated. Uninstalling (via "Apps & features", or `unins000.exe` in the
install directory) stops and removes the scheduled task and the binary; your
config, disk cache, and log file are left in place.

This installer path is optional and untested by the maintainers beyond a
source review of `windows/polipo.iss` - the fully manual steps in sections 4
and 8 remain the documented fallback if it misbehaves on your machine. Like
section 8, it is subject to the Smart App Control caveat above - the
installer deploys the same unsigned `polipo.exe`.
