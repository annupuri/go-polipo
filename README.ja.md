go-polipo (v2.0.0)
==================

(For the English version, see README.md / 英語版は README.md を参照してください)

go-polipo は、古典的なキャッシュ Web プロキシ Polipo を Go (v1.26.8) で
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
- **ディスクキャッシュ**: Polipo 互換のディレクトリ構成とファイル名ハッシュ
  (MD5 + カスタム Base64 テーブル)。`Set-Cookie` を含む応答は既定でキャッシュ
  しません(`dontCacheCookies`)。認証付きリクエストの応答は明示的に共有可能な
  場合のみキャッシュします(RFC 9111 §3.5)。`noDiskCache` または空の
  `diskCacheRoot` でディスクキャッシュ自体を無効化できます。
- **キャッシュ期限切れ処理**: `polipo -x` がディスク上のキャッシュを走査し、
  壊れたエントリ、(HTTP 鮮度ルール上)古くなったエントリ、長期間アクセスの
  ないエントリ(`diskCacheUnlinkTime`、既定 32 日)を削除し、空になった
  ディレクトリを整理します。
- **ローカル Web インターフェース**: `/polipo/` と `/polipo/config` から
  設定状態を動的に確認できます。

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
| `forbiddenFile` | (なし) | アクセス禁止リストのファイル |
| `uncachableFile` | (なし) | キャッシュ対象外リストのファイル |
| `socksParentProxy` | (なし) | 親 SOCKS プロキシ (`host:port`) |
| `parentProxy` | (なし) | 親 HTTP プロキシ (`host:port`) |
| `allowedClients` | `127.0.0.1, ::1` | 接続を許可するクライアント (IP / CIDR / `all`) |
| `tunnelAllowedPorts` | `443, 563` | CONNECT を許可するポート |
| `maxConcurrentRequests` | `100` | 同時処理リクエスト数の上限 (0 で無制限) |
| `logFile` | (なし) | ログの出力先ファイル。未指定なら標準エラー出力 |

ドキュメント
------------

- ビルドとインストール手順は `INSTALL.ja.md` を参照してください(「Windows
  でのビルド」「Windows 起動時の自動実行」節を含みます)。
- アーキテクチャの詳細と C から Go へのファイル対応表は `ARCHITECTURE.ja.md`
  を参照してください。

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
