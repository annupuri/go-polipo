go-polipo (v2.0.0) インストール手順
=============================================

(For the English version, see INSTALL.md / 英語版は INSTALL.md を参照してください)

これは Polipo キャッシュ Web プロキシの Go 実装です。

1. 必要要件
-----------

go-polipo のビルドには Go(バージョン 1.27.3 以降)が必要です。

2. ビルドと実行
----------------

同梱の Makefile でビルドできます:

    $ make build

既定では `bin/polipo`(Windows では `bin/polipo.exe`)にバイナリが生成されます。

`/usr/local/bin` にインストールする場合(POSIX システム):

    $ sudo make install

プロキシを起動:

    $ ./bin/polipo

既定ではフォアグラウンドで動作し、`http://127.0.0.1:8123/` で待ち受けます。

3. Web ブラウザの設定
----------------------

Web ブラウザのプロキシ設定を `http://localhost:8123/` に向けてください。
環境変数でも設定できます:

    $ export http_proxy=http://127.0.0.1:8123
    $ export https_proxy=http://127.0.0.1:8123

### Windows: Microsoft Store などのストアアプリ

Windows のシステムプロキシを `127.0.0.1:8123` に向けると、Microsoft Store(や他のパッケージ化された UWP アプリ)がエラーになることがあります。プロキシをオフにすると正常に動きます。これは go-polipo の問題ではなく、元の Polipo でも、go-polipo をネイティブで動かしても WSL 内で動かしても同じように起きます。これらのアプリは AppContainer というサンドボックス内で動いており、Windows は既定で AppContainer からループバックアドレスへの接続を禁止しているため、リクエストがプロキシまで届きません(プロキシのログにも何も出ません)。

**管理者権限の**プロンプトで、そのアプリをループバック分離の対象外にします:

    CheckNetIsolation LoopbackExempt -a '-n=Microsoft.WindowsStore_8wekyb3d8bbwe'

PowerShell では引用符を外さないでください。引用符が無いと PowerShell が引数を `.` の位置で分割してしまい、CheckNetIsolation が「パラメーターが無効です」で失敗します。cmd.exe では引用符は不要です。再起動は不要で、アプリを開き直すだけで反映されます。`CheckNetIsolation LoopbackExempt -s` で対象外リストを表示でき、`-a` の代わりに `-d` を使うと削除できます。他のストアアプリの場合は、`Get-AppxPackage <名前> | Select-Object PackageFamilyName` でパッケージファミリ名を調べてください。

**go-polipo をミラーモードの WSL 内で動かしている場合**(セクション8): この構成では 127.0.0.1:8123 で待ち受けているのは Windows のプロセスではなく WSL VM 内の go-polipo で、ループバック分離の除外がこの経路には効かないことがあります。Windows 11 26H2(ビルド 26300)へのアップデート後に、次の状態が確認されました: 除外は一覧に残っており、削除して登録し直しても改善せず、Store は「Microsoft Store は接続を完了できませんでした」で失敗し(イベントログ Microsoft-Windows-Store/Operational に `0x80072EFD`)、Store のプロセス(`WinStore.App`)はプロキシへの接続を一切開いていませんでした。この場合は Store の通信をプロキシから外します。設定 > ネットワークとインターネット > プロキシ > 「プロキシ サーバーを使う」の「編集」で、「次のエントリで始まるアドレス以外にプロキシ サーバーを使います」の欄に次を追加します:

    *.microsoft.com;*.s-microsoft.com;*.xboxservices.com;*.xboxlive.com

これで、Store が1週間の間に接続したすべてのホスト(`microsoft.com` 配下の `storeedge`・`licensing.mp`・`displaycatalog`・`sfdataservice`・`login` など、および `da.xboxservices.com`・`images-eds-ssl.xboxlive.com`)をカバーできます。Store の通信は CONNECT 経由の HTTPS で、go-polipo はもともとキャッシュしないため、プロキシを外しても失うものはありません。なお、他のアプリケーションもこれらのドメインへは直接接続するようになります。設定後も失敗し続けた Store の通信は広告のトラッキング(Microsoft Advertising の Universal Event Tracking。イベントログ上は「UET」)の送信だけで、害はありません。広告の表示やクリックを広告主に報告するためのものです。UET の送信先は通常 `bat.bing.com` なので、これも通したい場合は一覧に `*.bing.com` も追加すれば通るはずです。

4. go-polipo の設定
--------------------

ディスクキャッシュを使う場合は、キャッシュディレクトリが存在し書き込み可能であることを確認してください:

    $ mkdir -p /var/cache/polipo/

設定ファイルは以下の標準的な場所に置けます:
- `~/.polipo`
- `/etc/polipo/config`

`-c` フラグで任意の設定ファイルパスを指定することもできます:

    $ ./bin/polipo -c /path/to/config

サポートされている変数とその既定値は `config.sample` を参照してください。実際に反映されている設定は `./bin/polipo -v` で、またはプロキシ実行中に `http://127.0.0.1:8123/polipo/config` のローカル設定ページでも確認できます。

5. Windows でのビルド
----------------------

Go は追加のツールなしで Windows バイナリをビルドできます。Windows 上で go-polipo をコンパイル・実行するのに Cygwin、MinGW、その他の POSIX エミュレーションレイヤーは不要です。

ネイティブビルド(Go インストール済みの PowerShell または コマンドプロンプト):

    > make build

`make` を使わない場合:

    > go build -o bin\polipo.exe .

Unix/Linux/macOS/WSL から Windows 向けにクロスコンパイル:

    $ make build-windows                  # -> bin/polipo.exe (amd64)
    $ make build-windows WINARCH=arm64    # -> bin/polipo.exe (arm64)
    $ make build-windows WINGUI=1         # 起動時にコンソール窓を出さない

`make` を使わない場合:

    $ GOOS=windows GOARCH=amd64 go build -o bin/polipo.exe .

WSL 上で `make build` を実行するとネイティブの Linux 用 `bin/polipo` が生成されます。`bin/polipo.exe` が欲しい場合は `build-windows` を使うか `GOOS=windows` を指定してください。

パスについて: C 言語版と異なり、go-polipo は実行環境のパス表記を自動的に扱います。設定ファイル内の `diskCacheRoot` など各種パスは、有効な Windows パス(例: `C:\var\cache\polipo\`)にしてください。

起動時に自動実行する場合: `WINGUI=1` でビルドしてコンソール窓を出さないようにし、設定ファイルに `logFile` を指定(`-H windowsgui` ビルドはログ出力先のコンソールを持たないため)した上で、タスクスケジューラに「ログオン時」トリガーで `polipo.exe -c <config>` を実行するタスクを登録してください。検証済みの完全な手順は下記セクション 6 を参照してください。

6. Windows 起動時に go-polipo を自動実行する
----------------------------------------------

**スマートアプリコントロールが「オン」の場合、現状は非推奨です。** Windows 11のスマートアプリコントロールは、一度「オン」に固定される(「評価」の選択肢が選べなくなる)と、未署名の自前ビルドバイナリ(`polipo.exe`など)の実行を「アプリケーション制御ポリシーによってこのファイルがブロックされました」というエラーで一律ブロックします。これをWindowsのクリーンインストール以外の方法で「オフ」に戻す公式な手段はなく、自己署名証明書での回避もできません。該当する場合は、このセクションを飛ばしてセクション8(WSL上での実行)に進んでください。本セクションは、スマートアプリコントロールが「オフ」または「評価中」の環境、あるいはバイナリに正式なコード署名がある場合向けに残しています。

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

コンソール窓の出ないバイナリをビルド(セクション 5 参照)し、生成された `bin\polipo.exe` を `%LOCALAPPDATA%\Programs\go-polipo\polipo.exe` に配置します:

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
    # 新しくビルドした bin\polipo.exe を配置場所に上書きコピーしてから:
    Start-ScheduledTask -TaskName "go-polipo"

タスク開始時にコンソール窓が表示される場合、配置した polipo.exe に `WINGUI=1` を付け忘れてビルドした、通常のコンソール版になっています。その窓を閉じると、閉じた通知を受けて go-polipo 自体も道連れで終了してしまい、ログにエラーは残らないまま「プロキシが突然使えなくなる」症状として現れます。セクション 5 の `WINGUI=1` でビルドし直し、上記の手順で入れ替えてください。

7. Windows インストーラー(任意)
--------------------------------

セクション6や「バイナリの更新」を毎回手作業で行う代わりに、[Inno Setup](https://jrsoftware.org/isinfo.php)でインストーラー/アンインストーラーを1つにまとめることもできます。リリース成果物としては配布しておらず、`windows/polipo.iss`から各自のローカル環境でビルドする想定です。

Inno Setupを一度インストールした上で、リポジトリのルートから:

    $ make build-windows WINGUI=1
    $ iscc windows/polipo.iss

(`make installer` でこの2行をまとめて実行できます。) `windows/dist/go-polipo-setup.exe` が生成されます。これを実行する(タスクスケジューラへの登録に管理者権限が必要なため、インストーラー自体も管理者権限で実行されます)と、セクション4と6で手作業だった内容が一通り行われます:

- `polipo.exe` を `%LOCALAPPDATA%\Programs\go-polipo\` に配置
- `%USERPROFILE%\.polipo` が存在しなければ `config.sample` から作成(既存の設定は上書きしません)
- セクション6の `go-polipo` タスクスケジューラ登録と起動

インストーラーを(コード変更後の再ビルドなどで)再度実行すると、上書きの前に稼働中のタスク/プロセスを自動的に停止してからバイナリを入れ替えます。手動更新時の`Stop-ScheduledTask`/`Stop-Process`と同じ処理を自動化しているだけです。アンインストール(「アプリと機能」またはインストール先の`unins000.exe`から)では、タスクスケジューラの登録解除とバイナリの削除のみを行い、設定ファイル・ディスクキャッシュ・ログファイルはそのまま残します。

このインストーラー経由の手順は任意のもので、`windows/polipo.iss`のソースレビュー以上の動作確認は行っていません。うまく動かない場合はセクション4・6の手動手順が引き続き正式な手段です。セクション6と同様、このインストーラーが配置するのも同じ未署名の`polipo.exe`なので、上記のスマートアプリコントロールに関する注記が当てはまります。

8. WSL上でgo-polipoを実行する(スマートアプリコントロールの回避策)
--------------------------------------------------------------------

自前ビルドの`polipo.exe`がWindows側で一律ブロックされてしまう場合(セクション6のスマートアプリコントロールに関する注記を参照)、WSL2上でネイティブのLinux版バイナリを動かすことで、コード署名の取得やWindowsの再インストールを待たずにgo-polipoをプロキシとして使い続けられます。WSL2は既定で`127.0.0.1`をWindowsとLinux VM間で転送するため、Windows側のアプリケーションは今まで通り`http://127.0.0.1:8123/`を指すだけで済みます。

WSL上でネイティブのLinuxバイナリをビルドします:

    $ make build

(WSL上での既定の`make build`はそのままLinux用バイナリを生成します。`GOOS`指定や`build-windows`は不要です。セクション5参照。)

設定・キャッシュ・ログはWSLのファイルシステム内に置きます(例: `~/.polipo`):

    proxyAddress = 127.0.0.1
    proxyPort = 8123
    diskCacheRoot = /home/<you>/.cache/go-polipo/cache
    logFile = /home/<you>/.cache/go-polipo/polipo.log

Windowsの場合と同様、`diskCacheRoot`はgo-polipoが初回利用時に自動作成しますが、`logFile`の親フォルダは自動作成しないので、あらかじめ作成してください:

    $ mkdir -p ~/.cache/go-polipo

### systemdサービスとして実行する

最近のWSL2ディストリビューションの多くは既定でsystemdが有効です。次のコマンドで確認できます:

    $ ps -p 1 -o comm=

`systemd`と表示されれば、サンプルのユニットファイル`systemd/go-polipo.service`をコピーし、中の`<you>`と`/path/to/go-polipo`を置き換えます:

    $ cp systemd/go-polipo.service ~/go-polipo.service
    $ sed -i "s|<you>|$USER|g; s|/path/to/go-polipo|$PWD|" ~/go-polipo.service

このファイルの`RestartSec=5`と`StartLimitIntervalSec=0`は、ポート8123がまだ他に掴まれている間もsystemdに再試行を続けさせるための設定です。ミラーモードのネットワークでは、WSLの再起動後に最大2分ほどこの状態になります(後述の「IPv6とミラーモードのネットワーク」参照)。これらが無いと、systemdの既定値(100ms後に再試行し、10秒以内に5回起動したら諦める)により、1秒足らずでサービスが恒久的に停止してしまいます。

配置して有効化します:

    $ sudo cp ~/go-polipo.service /etc/systemd/system/go-polipo.service
    $ sudo systemctl daemon-reload
    $ sudo systemctl enable --now go-polipo
    $ sudo systemctl status go-polipo --no-pager

### WSL2 VM(とサービス)を落とさないようにする

WSL2は既定で、最後のWSLターミナル/ウィンドウを閉じてからしばらくするとバックグラウンドVM自体を終了させ、systemdごとgo-polipoも道連れに停止します。**Windows側**の`%UserProfile%\.wslconfig`に以下を追記してこれを無効化してください:

    [general]
    instanceIdleTimeout=-1

    [wsl2]
    vmIdleTimeout=-1

`vmIdleTimeout`はVMを、`instanceIdleTimeout`はWSLのセッションが無くなった後のディストリビューション自体を落とさないための設定です。既定のNATモードではこれが無くても(WSLのlocalhost転送が動いているため)たまたまディストリビューションが動き続けますが、ミラーモードでは必須です。無いと、後述のログオン時のショートカットが実行されてから約15秒でディストリビューションがgo-polipoごと停止します。

### Windowsログオン時にWSLを自動起動する

WSL2のVMはWindowsログオン時に自動では起動しないため、Microsoft署名済み(スマートアプリコントロールの対象外)の`wsl.exe`を呼び出すスタートアップフォルダのショートカットで一度起こしてやる必要があります。これによりsystemd経由でgo-polipoも起動します。**Windows PowerShell**で:

    $startup = [Environment]::GetFolderPath('Startup')
    $ws = New-Object -ComObject WScript.Shell
    $sc = $ws.CreateShortcut("$startup\go-polipo-wsl.lnk")
    $sc.TargetPath  = "$env:WINDIR\System32\wsl.exe"
    $sc.Arguments   = "-d <ディストリビューション名> -u <you> -- true"
    $sc.WindowStyle = 7
    $sc.Save()

Windows再起動後に確認:

    Test-NetConnection 127.0.0.1 -Port 8123

### IPv6とミラーモードのネットワーク

WSLの既定のNATモードでは、Windows側にIPv6接続があってもLinux VMにはIPv6接続がありません。このため、IPv6(AAAA)アドレスしか持たないホストにはgo-polipo経由で接続できず、ログには次のように出ます:

    CONNECT failed: dial example.com:443: dial tcp [2606:...]:443: connect: network is unreachable

(プロキシをオフにすればWindowsからは接続できます。)ミラーモードにすると、WSLがWindowsと同じIPv4・IPv6アドレスを使うようになります。Windows 11 22H2以降が必要です。`%UserProfile%\.wslconfig`に、前述の`instanceIdleTimeout`と合わせて設定します:

    [general]
    instanceIdleTimeout=-1

    [wsl2]
    vmIdleTimeout=-1
    networkingMode=mirrored

ミラーモードで変わる点:

- **WindowsとWSLでポート番号の空間を共有します。** Windows側のプロセスが127.0.0.1:8123で待ち受けていると、go-polipoは起動できません(ログに`bind: address already in use`)。セクション6のタスクやショートカットが無効になっていることを確認してください。
- **Windows側でTIME_WAIT状態のポートも使用中とみなされます。** NATモードでは、Windows側からポート8123への接続をWSLのlocalhost転送(`wslrelay.exe`)が受けています。`wsl --shutdown`の後、それらの接続はWindowsの既定で最大2分間TIME_WAITとして残ります。このため、NATモードからミラーモードに切り替えた直後や、その後WSLを再起動した直後は、最大2分ほどgo-polipoが待ち受けを開始できません。その間、systemdが再試行を続けるため`systemctl is-active go-polipo`は`activating`と表示され、その後`active`になります(ユニットファイルに前述の`RestartSec`/`StartLimitIntervalSec`がある場合)。Windowsの起動直後には起きません。
- **Microsoft Storeの通信をプロキシから外す必要があることがあります。** セクション3のループバック分離の除外だけでは足りない場合があります。セクション3の末尾を参照してください。
- **IPv4とIPv6の両方を持つホストではIPv6が優先されることがあります。** go-polipo(Goのダイヤラー)は両方を試して先に繋がった方を使うため、ログに出る接続先がIPv6アドレスになることがあります。

切り替え手順(**Windows PowerShell**で):

    wsl --shutdown
    # 上記のとおり %UserProfile%\.wslconfig を編集してから:
    wsl -e true
    Start-Sleep 150                               # TIME_WAITの待ち時間を見込む
    wsl -l -v                                     # STATE が Running
    wsl -e systemctl is-active go-polipo          # active
    Test-NetConnection 127.0.0.1 -Port 8123       # TcpTestSucceeded : True

WSL内で`ip -6 route`を実行すると、`default`の経路が表示されるはずです。元に戻すには、`networkingMode`の行を削除して再度`wsl --shutdown`します。

### バイナリの更新

Windowsと異なり、WSL/Linuxは実行中のファイルをロックしないため、上書き前に停止する手順は不要です:

    $ git pull
    $ make build
    $ sudo systemctl restart go-polipo

WSLに切り替える前にセクション6のタスクスケジューラ登録やスタートアップフォルダのショートカットを作成していた場合は、ポート8123を取り合わないよう無効化または削除してください:

    Disable-ScheduledTask -TaskName "go-polipo"   # 管理者権限で実行
