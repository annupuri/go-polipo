go-polipo (v2.0.0)
==================

(For the English version, see README.md / 英語版は README.md を参照してください)

go-polipo は、古典的なキャッシュ Web プロキシ Polipo を Go (v1.27.4) で
完全に書き直した再実装です。

オリジナルのシングルスレッド C 版とは異なり、go-polipo は Go ネイティブの
ゴルーチンを活用して高い並行性と性能を実現し、あわせてメモリ安全性の問題を
解消しています。

機能
----

- **HTTP/1.1 プロキシ**: keep-alive 接続とリクエスト転送に対応。
- **CONNECT トンネリング**: HTTPS トンネリングに対応(ポートフィルタリング設定つき)。
- **SOCKS 親プロキシ**: 上流リクエストを SOCKS5 プロキシ経由で転送。
- **禁止フィルタ**: ホスト / ドメインおよび正規表現によるブラックリスト。
- **宛先フィルタ**: このマシンやそのリンク(ループバック、リンクローカル、未指定アドレス、マルチキャスト)
  への転送と CONNECT を 403 で拒否する(`forbidLocalDestinations`、既定で有効)。名前解決した後のアドレスで
  判定するので、127.0.0.1 に解決されるホスト名(DNS リバインディング)も拒否する。
  `forbidPrivateDestinations` を有効にすると、LAN のプライベートアドレスも拒否する。
- **ディスクキャッシュ**: Polipo 互換のディレクトリ構成とファイル名ハッシュ
  (MD5 + カスタム Base64 テーブル)。`Set-Cookie` を含む応答は既定でキャッシュ
  しません(`dontCacheCookies`)。認証付きリクエストの応答は明示的に共有可能な
  場合のみキャッシュします(RFC 9111 §3.5)。`noDiskCache` または空の
  `diskCacheRoot` でディスクキャッシュ自体を無効化できます。
- **キャッシュ期限切れ処理**: `polipo -x` がディスク上のキャッシュを走査し、
  壊れたエントリ、(HTTP 鮮度ルール上)古くなったエントリ、長期間アクセスの
  ないエントリ(`diskCacheUnlinkTime`、既定 32 日)を削除し、空になった
  ディレクトリを整理します。
- **ローカル Web インターフェース**: `/polipo/config` で設定内容を、
  `/polipo/status` で起動以降の統計（リクエスト数、キャッシュヒット数、
  304 による再検証の数、CONNECT トンネル数、エラー数など）を確認でき、
  再起動不要のランタイム操作として `/polipo/reload`（`forbiddenFile` /
  `uncachableFile` / `forbiddenTunnelsFile` の再読み込み）と
  `/polipo/flush`（`-x` と同じディスクキャッシュの期限切れ処理を実行）を
  用意しています。
  Host がこのマシンを指している(IP アドレス、`localhost`、ホスト名、`proxyAddress`)
  リクエストにだけ応答し、ほかのサイトからの POST は拒否するので、Web ページが
  DNS リバインディングや偽のフォームで管理画面を読んだり操作したりすることはできません。

コマンドラインオプション
------------------------

    polipo [ -h ] [ -v ] [ -x ] [ -c filename ] [ -- ] [ var=val... ]

    -h            使用方法を表示します。
    -v            バージョンと現在の設定内容を表示します。
    -x            ディスクキャッシュの期限切れ処理を実行して終了します。
    -c filename   filename から設定を読み込みます。

末尾に `var=val` 形式の引数を並べると設定ファイルの値を上書きできます。
例: `polipo proxyPort=8080 diskCacheRoot=/tmp/cache`

主な設定変数
------------

| 変数 | 既定値 | 説明 |
| :--- | :--- | :--- |
| `proxyAddress` | `127.0.0.1` | 待ち受けアドレス |
| `proxyPort` | `8123` | 待ち受けポート |
| `diskCacheRoot` | `/var/cache/polipo/` | ディスクキャッシュのルートディレクトリ (空でキャッシュ無効) |
| `noDiskCache` | `false` | `true` でディスクキャッシュを完全に無効化 |
| `dontCacheCookies` | `true` | `Set-Cookie` を含む応答をキャッシュしない |
| `diskCacheUnlinkTime` | `32d` | 最終アクセスからこの期間を過ぎたキャッシュを `-x` で削除 |
| `maxDiskCacheEntrySize` | `-1`(無制限) | この値(バイト)を超える応答本文はキャッシュしない(転送は行う) |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | `4d12h` / `1048576` | `-x` 実行時、`diskCacheTruncateSize` より大きく `diskCacheTruncateTime` 以上未使用のエントリを早期削除(Polipoは切り詰めて残すが、部分インスタンス機構が無いgo-polipoでは安全に切り詰められないため削除に変更) |
| `forbiddenFile` | (なし) | アクセス禁止リストのファイル |
| `uncachableFile` | (なし) | キャッシュ対象外リストのファイル |
| `forbiddenTunnelsFile` | (なし) | CONNECT (HTTPS トンネル) 用のホスト単位ブロックリスト。`forbiddenFile` とは別扱いで、こちらはサブドメインに広がらない完全一致のみ |
| `socksParentProxy` | (なし) | 親 SOCKS プロキシ (`host:port`) |
| `parentProxy` | (なし) | 親 HTTP プロキシ (`host:port`)。HTTPS (CONNECT) も Polipo と同じくこの親を経由する |
| `allowedClients` | `127.0.0.1, ::1` | 接続を許可するクライアント (IP / CIDR / `all`)。ループバック以外で待ち受け、`all` を指定し、`authCredentials` を設定していないと、ポートに届く人なら誰でも使えるため、起動時に警告を出す |
| `tunnelAllowedPorts` | `22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418` | CONNECT を許可するポート |
| `forbidLocalDestinations` | `true` | このマシンやそのリンク(127.0.0.0/8、::1、0.0.0.0/8、169.254.0.0/16、fe80::/10、マルチキャスト)への転送と CONNECT を拒否する。名前解決した後のアドレスで判定する。親プロキシ経由のときは判定しない (Polipo には無い設定) |
| `forbidPrivateDestinations` | `false` | `true` にすると、プライベートアドレス(10.0.0.0/8、172.16.0.0/12、192.168.0.0/16、fc00::/7、100.64.0.0/10)への転送と CONNECT も拒否する |
| `maxConcurrentRequests` | `100` | 同時処理リクエスト数の上限 (0 で無制限) |
| `clientTimeout` | `120s` | クライアントがリクエストヘッダを送り終えるまでの時間と、リクエストの合間に接続を遊ばせておける時間の上限 (0 で無効) |
| `serverTimeout` | `90s` | 上流への接続、レスポンスヘッダの待ち、やり取りの途中の無通信の上限。時間切れは 504 (0 で無効) |
| `serverIdleTimeout` | `45s` | 使っていない上流への接続を再利用のために残しておく時間 (0 で無効) |
| `tunnelIdleTimeout` | `0`(無効) | CONNECT トンネルの両方向とも、この時間データが流れなければトンネルを閉じる。IMAP IDLE や放置した SSH のように長く黙る接続があるので、既定は Polipo と同じく無効 (Polipo には無い設定) |
| `disableVia` | `false` | `true` にすると、転送するリクエストとレスポンスに `Via` を付けない。既定では RFC 9110 に従って付ける (Polipo の既定は付けない) |
| `logFile` | (なし) | ログの出力先ファイル。未指定なら標準エラー出力 |
| `logFilePermissions` | `0640` | ログファイルを作るときの権限 (8進数。umask が差し引かれる)。既にあるファイルの権限は変えない |

ドキュメント
------------

- ビルドとインストール手順は `docs/INSTALL.ja.md` を参照してください。Windows では
  WSL の中で動かす方法を推奨しています(Windows 向けのビルドと、Windows 起動時の
  自動実行の手順も含みます)。
- アーキテクチャの詳細と C から Go へのファイル対応表は `docs/ARCHITECTURE.ja.md`
  を参照してください。
- 開発への参加方法は `CONTRIBUTING.md`(英語)を参照してください。

謝辞とライセンス
----------------

go-polipo は、Juliusz Chroboczek 氏らによるキャッシュ Web プロキシ
**Polipo**(<https://github.com/jech/polipo>)の独立した Go 再実装です。
オリジナルは開発を終了しています。go-polipo は Polipo のディスクキャッシュ
形式と設定ファイル構文を引き継ぎ、Polipo 1.1.1 の Git 履歴を出発点として
います。そのため、オリジナルの作者陣が本リポジトリの履歴および
Contributors 一覧に含まれています。

両プロジェクトとも MIT ライセンスで配布されています。詳細は `LICENSE` を
参照してください。Polipo の著作権表示はそのまま保持しています。
