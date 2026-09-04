go-polipo (v2.0.0) インストール手順
=============================================

(For the English version, see INSTALL.md / 英語版は INSTALL.md を参照してください)

これは Polipo キャッシュ Web プロキシの Go 実装です。

1. 必要要件
-----------

go-polipo のビルドには Go(バージョン 1.26.8 以降)が必要です。

2. ビルドと実行
----------------

同梱の Makefile でビルドできます:

    $ make build

既定では `polipo`(Windows では `polipo.exe`)という名前のバイナリが生成されます。

`/usr/local/bin` にインストールする場合(POSIX システム):

    $ sudo make install

プロキシを起動:

    $ ./polipo

既定ではフォアグラウンドで動作し、`http://127.0.0.1:8123/` で待ち受けます。

3. Web ブラウザの設定
----------------------

Web ブラウザのプロキシ設定を `http://localhost:8123/` に向けてください。
環境変数でも設定できます:

    $ export http_proxy=http://127.0.0.1:8123
    $ export https_proxy=http://127.0.0.1:8123

4. go-polipo の設定
--------------------

ディスクキャッシュを使う場合は、キャッシュディレクトリが存在し書き込み可能であることを確認してください:

    $ mkdir -p /var/cache/polipo/

設定ファイルは以下の標準的な場所に置けます:
- `~/.polipo`
- `/etc/polipo/config`

`-c` フラグで任意の設定ファイルパスを指定することもできます:

    $ ./polipo -c /path/to/config

サポートされている変数とその既定値は `config.sample` を参照してください。実際に反映されている設定は `./polipo -v` で、またはプロキシ実行中に `http://127.0.0.1:8123/polipo/config` のローカル設定ページでも確認できます。

5. Windows でのビルド
----------------------

Go は追加のツールなしで Windows バイナリをビルドできます。Windows 上で go-polipo をコンパイル・実行するのに Cygwin、MinGW、その他の POSIX エミュレーションレイヤーは不要です。

ネイティブビルド(Go インストール済みの PowerShell または コマンドプロンプト):

    > make build

`make` を使わない場合:

    > go build -o polipo.exe .

Unix/Linux/macOS/WSL から Windows 向けにクロスコンパイル:

    $ make build-windows                  # -> polipo.exe (amd64)
    $ make build-windows WINARCH=arm64    # -> polipo.exe (arm64)
    $ make build-windows WINGUI=1         # 起動時にコンソール窓を出さない

`make` を使わない場合:

    $ GOOS=windows GOARCH=amd64 go build -o polipo.exe .

WSL 上で `make build` を実行するとネイティブの Linux 用 `polipo` が生成されます。`polipo.exe` が欲しい場合は `build-windows` を使うか `GOOS=windows` を指定してください。

パスについて: C 言語版と異なり、go-polipo は実行環境のパス表記を自動的に扱います。設定ファイル内の `diskCacheRoot` など各種パスは、有効な Windows パス(例: `C:\var\cache\polipo\`)にしてください。

起動時に自動実行する場合: `WINGUI=1` でビルドしてコンソール窓を出さないようにし、設定ファイルに `logFile` を指定(`-H windowsgui` ビルドはログ出力先のコンソールを持たないため)した上で、タスクスケジューラに「ログオン時」トリガーで `polipo.exe -c <config>` を実行するタスクを登録してください。検証済みの完全な手順は下記セクション 6 を参照してください。

6. Windows 起動時に go-polipo を自動実行する
----------------------------------------------

推奨レイアウト(1 ユーザー単位。ファイル自体の配置に管理者権限は不要):

    %LOCALAPPDATA%\Programs\go-polipo\polipo.exe    実行ファイル
    %USERPROFILE%\.polipo                           設定ファイル(自動検出)
    %LOCALAPPDATA%\go-polipo\cache\                 diskCacheRoot
    %LOCALAPPDATA%\go-polipo\polipo.log             logFile

`diskCacheRoot` は go-polipo が初回利用時に自動作成しますが、**`logFile` の親フォルダは自動作成しません**。あらかじめ一度だけ作成してください:

    > mkdir "%LOCALAPPDATA%\go-polipo"

`%USERPROFILE%\.polipo` の設定例:

    proxyAddress = 127.0.0.1
    proxyPort = 8123
    diskCacheRoot = C:/Users/<you>/AppData/Local/go-polipo/cache
    logFile = C:/Users/<you>/AppData/Local/go-polipo/polipo.log

コンソール窓の出ないバイナリをビルド(セクション 5 参照)し、`%LOCALAPPDATA%\Programs\go-polipo\polipo.exe` に配置します:

    $ make build-windows WINGUI=1

### タスクスケジューラへの登録(推奨)

**管理者権限の** PowerShell で実行してください(タスクの登録自体に管理者権限が必要ですが、タスクの実行自体は通常の非特権アカウントで行われます。`-RunLevel Limited`):

    $exe = "$env:LOCALAPPDATA\Programs\go-polipo\polipo.exe"
    $cfg = "$env:USERPROFILE\.polipo"

    $action    = New-ScheduledTaskAction -Execute $exe -Argument "-c `"$cfg`"" -WorkingDirectory (Split-Path $exe)
    $trigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings  = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -MultipleInstances IgnoreNew
    $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited

    Register-ScheduledTask -TaskName "go-polipo" -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force

管理者権限で実行してもなお `Register-ScheduledTask` が "Access is denied" になる場合、組織管理端末ではポリシーでタスクスケジューラへの登録自体がブロックされていることがあります。その場合は下記のスタートアップフォルダ方式を代わりに使ってください。

### 起動確認

    Start-ScheduledTask -TaskName "go-polipo"
    Get-ScheduledTask -TaskName "go-polipo" | Get-ScheduledTaskInfo
    Test-NetConnection 127.0.0.1 -Port 8123
    Get-Content "$env:LOCALAPPDATA\go-polipo\polipo.log" -Tail 5

`LastTaskResult` は **タスクスケジューラ**のステータスコードであり、go-polipo 自身の終了コードではありません。`267009`(`0x41301`、「タスクは現在実行中」)は正常に稼働中であることを意味します。`0` は正常終了(`-h`/`-v`/`-x` 実行後など)を意味します。それ以外の小さな整数(`1`、`2` など)は go-polipo 自身の終了コードなので、理由はログファイルを確認してください。`TcpTestSucceeded : True` はプロキシが待ち受け中であることの確認になり、ログには "starting on http://..." の行が出るはずです。

### 代替手段: スタートアップフォルダへのショートカット(特別な権限不要)

    $exe = "$env:LOCALAPPDATA\Programs\go-polipo\polipo.exe"
    $cfg = "$env:USERPROFILE\.polipo"
    $startup = [Environment]::GetFolderPath('Startup')
    $ws = New-Object -ComObject WScript.Shell
    $sc = $ws.CreateShortcut("$startup\go-polipo.lnk")
    $sc.TargetPath       = $exe
    $sc.Arguments        = "-c `"$cfg`""
    $sc.WorkingDirectory = Split-Path $exe
    $sc.Save()

これで、権限昇格なしに、(`WINGUI=1` ビルドであれば)コンソール窓も出さずにログオン時に go-polipo が起動します。ただし、タスクスケジューラのようなクラッシュ時の自動再起動はありません。削除するには `Remove-Item "$startup\go-polipo.lnk"` を実行してください。

### バイナリの更新

Windows は実行中の実行ファイルをロックするため、上書きする前に go-polipo を停止してください:

    Stop-ScheduledTask -TaskName "go-polipo"
    Get-Process polipo -ErrorAction SilentlyContinue | Stop-Process -Force
    # 新しくビルドした polipo.exe を配置場所に上書きコピーしてから:
    Start-ScheduledTask -TaskName "go-polipo"
