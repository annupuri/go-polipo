go-polipo (v2.0.0) Installation Instructions
=============================================

(日本語版は INSTALL.ja.md を参照してください / A Japanese version is available in INSTALL.ja.md)

This is a Go-based implementation of the Polipo caching web proxy.

1. Requirements
---------------

To build go-polipo, you need to have Go (version 1.26.8 or later) installed on your system.

2. Building and running go-polipo
---------------------------------

You can build the binary using the provided Makefile:

    $ make build

By default, this will compile the binary named `polipo` (or `polipo.exe` on Windows).

To install the binary to `/usr/local/bin` (on POSIX systems):

    $ sudo make install

To run the proxy:

    $ ./polipo

By default, the proxy runs in the foreground and listens on http://127.0.0.1:8123/.

3. Configuring your web browser
-------------------------------

Configure your web browser to use the proxy on `http://localhost:8123/`.
You can also set the proxy via environment variables:

    $ export http_proxy=http://127.0.0.1:8123
    $ export https_proxy=http://127.0.0.1:8123

4. Configuring go-polipo
------------------------

If you want to use an on-disk cache, make sure the cache directory exists and is writable:

    $ mkdir -p /var/cache/polipo/

Configuration files can be placed in standard locations:
- `~/.polipo`
- `/etc/polipo/config`

You can also specify a custom configuration file path using the `-c` flag:

    $ ./polipo -c /path/to/config

`config.sample` lists the supported variables with their defaults. You can
also see the effective configuration with `./polipo -v`, or open the local
configuration page at `http://127.0.0.1:8123/polipo/config` while the proxy
is running.

5. Building on Windows
---------------------

Go builds Windows binaries out of the box. You do not need Cygwin, MinGW, or
any third-party POSIX emulation layer to compile or run go-polipo on Windows.

Native build (PowerShell or Command Prompt, with Go installed):

    > make build

or, without `make`:

    > go build -o polipo.exe .

Cross-compiling for Windows from Unix/Linux/macOS/WSL:

    $ make build-windows                  # -> polipo.exe (amd64)
    $ make build-windows WINARCH=arm64    # -> polipo.exe (arm64)
    $ make build-windows WINGUI=1         # no console window on launch

or without `make`:

    $ GOOS=windows GOARCH=amd64 go build -o polipo.exe .

`make build` on WSL produces the native Linux `polipo`; use `build-windows`
(or set `GOOS=windows`) to get `polipo.exe`.

Paths: unlike the C version, go-polipo follows the host's path conventions
automatically. Make sure `diskCacheRoot` and any other paths in your
configuration file are valid Windows paths, e.g. `C:\var\cache\polipo\`.

Running at startup: build with `WINGUI=1` so no console window appears, set
`logFile` in the configuration (a `-H windowsgui` build has no console for
its log output), then register a Task Scheduler task with an "At log on"
trigger pointing at `polipo.exe -c <config>`. See section 6 below for the
full, tested procedure.

6. Running go-polipo at Windows startup
----------------------------------------

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

Build a console-free binary (see section 5) and copy it to
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
    # copy the newly built polipo.exe into place, then:
    Start-ScheduledTask -TaskName "go-polipo"

If a console window pops up when the task starts, the binary you just built
and copied in is missing `WINGUI=1` - it's a regular console-subsystem
executable, not the console-less one. Closing that window sends it a close
signal that terminates go-polipo along with it, which looks like "the proxy
randomly stops working" with no error in the log. Rebuild with `WINGUI=1`
(section 5) and repeat the steps above.

7. Windows installer (optional)
--------------------------------

Sections 6 and "Updating the binary" above can be done by hand every time, or
packaged into a one-time installer/uninstaller built with
[Inno Setup](https://jrsoftware.org/isinfo.php). This is not distributed as a
release asset - you build it yourself, locally, from `windows/polipo.iss`.

Install Inno Setup once, then from the repo root:

    $ make build-windows WINGUI=1
    $ iscc windows/polipo.iss

(`make installer` runs both steps.) This produces
`windows/dist/go-polipo-setup.exe`. Running it (admin privileges are required,
since registering the scheduled task needs them) does everything sections 4
and 6 describe by hand:

- deploys `polipo.exe` to `%LOCALAPPDATA%\Programs\go-polipo\`
- creates `%USERPROFILE%\.polipo` from `config.sample` if one doesn't already
  exist (an existing config is never overwritten)
- registers and starts the `go-polipo` scheduled task from section 6

Running the installer again (e.g. after rebuilding with a code change) stops
the running task/process first, then overwrites the binary - the same
`Stop-ScheduledTask` / `Stop-Process` sequence as manual updates, just
automated. Uninstalling (via "Apps & features", or `unins000.exe` in the
install directory) stops and removes the scheduled task and the binary; your
config, disk cache, and log file are left in place.

This installer path is optional and untested by the maintainers beyond a
source review of `windows/polipo.iss` - the fully manual steps in sections 4
and 6 remain the documented fallback if it misbehaves on your machine.
