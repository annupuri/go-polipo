# Polipo 1.1.1 → go-polipo 機能パリティ監査

(For the English version, see PARITY_AUDIT.md / 英語版は PARITY_AUDIT.md を参照して
ください)

**日付:** 2026-09-10
**基準:** Polipo 1.1.1（`jech/polipo`）、そのマニュアル（`polipo.texi`）とソース。
**対象:** go-polipo 2.0.0（`config.go`, `cache.go`, `proxy.go`, `main.go`,
`forbidden.go`）。2026-09-10 にマージしたディスクキャッシュのハードニング
（`dontCacheCookies`、`noDiskCache`、認証付き応答のルール）を含む。
**範囲:** オリジナルがユーザーに公開していたもの（設定変数、CLI、シグナル、
ローカルページ、文書化された挙動）と、Go 版が現在実際に行うことの比較。
Go 標準ライブラリが吸収したランタイム内部は「委譲した」と記すためだけに挙げる。

このドキュメントはトリアージ用の棚卸しである。対応方針は提示しない。末尾の
「トリアージ用ショートリスト」で、公開前にどれだけ重要かを段階分けしている。

## 凡例

| 記号 | 意味 |
| :--: | :--- |
| ✅ | 実装済み。挙動は Polipo と同等 |
| 🔷 | Go ランタイム / 標準ライブラリへ意図的に委譲。ユーザー面での欠落なし |
| ⚠️ | 部分的・簡略化、または **パースするだけで強制していない**（死んだ設定） |
| ❌ | 未実装 — 実際の挙動ギャップ |

---

## 1. コマンドラインオプション

| オプション | 状態 | 備考 |
| :--- | :--: | :--- |
| `-h`, `-v`, `-x`, `-c` | ✅ | すべて実装（`main.go`）。 |
| `-v` の出力 | ✅ | `Config.Entries()` から生成し、`/polipo/config` と共有。 |

## 2. 待ち受けソケットとクライアントアクセス

| 変数 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `proxyAddress` | `127.0.0.1` | ✅ | |
| `proxyPort` | `8123` | ✅ | |
| `allowedClients` | (なし) | ✅ | 素の IP、CIDR、`all`/`*`。実際の TCP ピアアドレスで判定。 |
| `authCredentials` | (null) | ❌ | **プロキシ認証なし。** go-polipo はクライアントを IP でのみ認可し、`407` / `Proxy-Authenticate` チャレンジは存在しない。ARCHITECTURE.md では `auth.c` を「標準ライブラリ + proxy.go」に対応づけているが、チャレンジのロジックは無い。 |
| `authRealm` | (可変) | ❌ | 同上。 |

## 3. 外向き接続

| 変数 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `proxyOutgoingAddress` | OS 既定 | ❌ | 上流ソケットの送信元アドレスを指定できない（マルチホーム環境）。 |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 の一時 IPv6 送信元選択 — OS 任せ。 |
| `allowedPorts` | `80-100, 1024-65535` | ✅ | **強制済み（2026-09-11 解決）。** `handleForward` が対象ポートを求め（`requestPort`）、`AllowedPorts` に無ければ（`PortSet.Contains`）キャッシュ参照や上流ダイヤルの前に `403` で拒否する。既定は Polipo と同じ（`80-100, 1024-65535`）に戻したので、80–100 以外の特権ポート（25, 110, 119, 143, …）は拒否される。`parsePortSet` は Polipo の `lo-hi` レンジ構文（`-100`・`1024-` の開区間も）を受け付ける。CONNECT は Polipo 同様 `tunnelAllowedPorts` のみで判定し、2 つのリストは併用しない。 |

## 4. URL フィルタとリダイレクト

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | ドメインサフィックス + POSIX 風正規表現 → `403`（`forbidden.go`）。 |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | 同じマッチャ。該当 URL は転送するがキャッシュしない。 |
| `forbiddenUrl` | (null) | ❌ | ブロックしたリクエストを案内ページへリダイレクトできず、常に素の `403`。 |
| `forbiddenRedirectCode` | `302` | ❌ | 同上。 |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ❌ | ホスト単位の CONNECT ブロックリストなし。CONNECT は**ポートのみ**（`tunnelAllowedPorts`）でフィルタされ、許可ポートなら任意のホストへ到達できる。 |
| `redirector` / `redirectorRedirectCode` | (未設定) | ❌ | Squid 風の外部リダイレクタ連携なし。 |
| これらファイルの実行時リロード | `SIGUSR1`/`SIGUSR2` | ❌ | リストは起動時に一度読むだけ。再起動なしのリロード不可（§13 参照）。 |

## 5. 親プロキシ

| 変数 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (未設定) | ✅ | `http.Transport.Proxy` 経由の HTTP 親プロキシ。 |
| `parentAuthCredentials` | (未設定) | ❌ | HTTP 親プロキシへの認証不可。 |
| `socksParentProxy` | (未設定) | ✅ | `golang.org/x/net/proxy` 経由。 |
| `socksProxyType` | `socks5` | ✅ | **`socks5` のみ受理（2026-09-11 解決）。** `golang.org/x/net/proxy` は SOCKS5 専用。`Config.Set` が `socks4a` やタイプミスを起動時エラーで弾くようにした（従来は保持だけして結局 SOCKS5 で接続）。SOCKS4/4a は意図的に非実装 — RFC が無く、認証も IPv6 も無く、現代の SOCKS プロキシで必要とされない。 |
| `socksAuthCredentials` | (未設定) | ❌ | SOCKS のユーザー / パスワード認証なし。 |

## 6. トンネリング（CONNECT）

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | `handleConnect` で強制。go-polipo 既定は `443, 563` のみで Polipo より狭い。 |
| 双方向ハーフクローズのトンネル | — | ✅ | `tunnel()` が両方向の終了を待つ。CONNECT 上の HTTP/2 に対応。 |
| CONNECT とリクエスト同時実行数制限 | — | ✅ | CONNECT を `maxConcurrentRequests` から意図的に除外（最近の修正）。 |

## 7. HTTP ヘッダの扱いとプライバシー

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (空) | ❌ | 設定可能なヘッダ除去（送受信）なし。 |
| `censorReferer` | `false` | ❌ | `Referer` の抑制 / 短縮ができない。 |
| `disableVia` | `true`（Via なし） | ⚠️ | go-polipo は `Via` を一切付けない — Polipo の*既定*と一致 — が、**有効化する手段がなく**、プロキシループ検出も無い。 |
| `proxyName` / `displayName` | ホスト名 / `Polipo` | ❌ | プロキシ識別名を設定できない。エラー本文は Go の平文 `http.Error`。 |
| `X-Forwarded-For` | — | ✅ | 明示的に nil 化し、クライアント IP を転送しない（`Director`）。 |
| ホップバイホップヘッダの除去 | — | ✅ | `httputil.ReverseProxy` が RFC 7230 に従い処理。 |
| `alwaysAddNoTransform` | `false` | ❌ | 些末。 |
| `laxHttpParser`, `bigBufferSize`, `expectContinue` | — | 🔷 | パースは `net/http` の責務。`ExpectContinueTimeout` はトランスポート内で 1s 固定。 |

## 8. キャッシュ — 鮮度と検証

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | `isExpired` で尊重。`s-maxage` も尊重し、共有キャッシュである本実装では優先。不正な `Expires`（例 `0`）は失効済み扱い（RFC 9111 §5.3）。 |
| リクエストの `Cache-Control: no-cache`/`no-store` | — | ✅ | ルックアップをスキップ。 |
| レスポンスの `no-store` / `private` | — | ✅ | ストアをスキップ。 |
| **条件付き再検証** | — | ❌ | `If-None-Match` / `If-Modified-Since` なし。`cache.go` のコメント曰く *"we treat expired cache as a miss."*。期限切れエントリは毎回まるごと再取得し、`304` の経路が存在しない。Polipo の透過性モデルは再検証を前提にしている。 |
| **ヒューリスティック鮮度**（`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`） | (可変) | ⚠️ | **実装済み・設定不可（2026-09-11 解決）。** `Expires` / `max-age` / `s-maxage` の無い `200` は `isExpired` でヒューリスティック寿命 `min(0.1 × (Date − Last-Modified), 24h)`（`Last-Modified` 無しなら 24h 固定）を得る。`Lookup` と `-x` の両方に効く。**`maxAge` / `maxAgeFraction`：** Polipo は設定変数だったが go-polipo は同じ既定値をハードコード — 運用ポリシーではなく実質 RFC 由来の定数で、緩い値にすると今回直した過剰配信が復活するため（`cache.go` のコメント参照）。**`maxExpiresAge`**（未来の `Expires`/`max-age` をどこまで信用するかの上限）：先送り — `diskCacheUnlinkTime`（32日）が失効を頭打ちにするが、継続アクセスされるエントリには不完全。**`maxNoModifiedAge`**：条件付き再検証 / `304` が無い限り無意味（そのギャップは B 章）。 |
| `preciseExpiry` | `false` | 🔷 | 上記が無い以上、該当なし。 |
| `cacheIsShared` | `true` | ⚠️ | トグルは無いが、共有キャッシュのルールは部分的に強制されている。`private` は常にスキップ。認証付き応答は明示的に共有可能でない限りスキップ（ディスクキャッシュのハードニング）。 |
| `relaxTransparency` | `false` | ❌ | 「上流失敗時に stale を配信する」モードなし。 |
| `proxyOffline` | `false` | ❌ | オフライン / キャッシュ専用モードなし。 |

## 9. キャッシュ — Vary とコンテンツネゴシエーション

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `Vary` の扱い | — | ⚠️ | **安全側で対応、完全実装ではない（2026-09-11 解決）。** キャッシュキーは URL のみ（`md5(url)`）のままで変種の *マッチング* はしない。代わりに、`Vary` が空でない応答は書き込まず（`Store`）、継承したキャッシュツリー内にそういうエントリがあっても配信しない（`Lookup`）。誤った変種を返すバグは起きない。代償: `Vary` 応答（`Vary: Accept-Encoding` は非常に多い）は毎回上流へ取りに行く。変種ごとのキャッシュ（Polipo 相当）は将来の項目。 |
| `mindlesslyCacheVary` | `false` | ❌ | `Vary` 応答は一切キャッシュしないので無意味。 |
| `dontTrustVaryETag` | `maybe` | ❌ | 同上。 |
| `dontCacheCookies` | (off) | ✅ | 実装済み（ディスクキャッシュのハードニング）。go-polipo では Polipo と異なり **既定 on**。 |
| `dontCacheRedirects` | (可変) | ⚠️ | 設定不可。`Store` は `200` しかキャッシュしないため `301`/`302` はそもそもキャッシュされない（Polipo より厳しいが、ノブは無い）。 |

## 10. インメモリキャッシュ、部分コンテンツ、PMM

| 機能 | 状態 | 備考 |
| :--- | :--: | :--- |
| インメモリオブジェクトキャッシュ（LRU、ハッシュテーブル、`chunkHighMark`/`objectHighMark`/…） | ❌ | **インメモリキャッシュは一切なし。** `CacheManager` はヒットを毎回ディスクから読み、ストアを毎回ディスクへ書く。`chunk*` / `object*` / `idleTime` / `maxObjectsWhenIdle` に対応物なし。 |
| 部分インスティレーション（持っているバイト範囲を保存し、後で隙間を埋める） | ❌ | `Store` は `200 OK` の本文全体しかキャッシュせず、`206 Partial Content` は保存しない。 |
| レンジリクエストの組み立て / キャッシュからのレンジ配信 | ❌ | クライアントの `Range` リクエストは素通し。キャッシュは満たしも記録もしない。 |
| Poor Man's Multiplexing（`pmmSize`, `pmmFirstSize`） | ❌ | 大きなオブジェクトの分割取得なし。 |
| パイプライン / サーバー能力の探索（`serverSlots*`, `smallRequestTime`, `replyUnpipeline*`, `maxPipelineTrain`, `maxSideBuffering`, `serverExpireTime`） | 🔷 | コネクション再利用と多重化は `http.Transport` の責務（`MaxIdleConns:100`、`IdleConnTimeout:90s` 固定）。設定では調整不可。 |

## 11. ディスクキャッシュ

| 変数 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `diskCacheRoot` | `/var/cache/polipo/` | ✅ | 同じ階層構造と MD5+base64 ファイル名。Polipo のキャッシュツリーを再利用可能。空値でキャッシュ無効（ディスクキャッシュのハードニング。従来は `/` に正規化されていた）。 |
| `noDiskCache` | — (go-polipo 独自) | ✅ | 新規。ルックアップと書き込みを無効化。 |
| `diskCacheUnlinkTime` | `32d` | ✅ | `-x`（`Expire`）で強制。空ディレクトリの整理つき。 |
| `maxDiskCacheEntrySize` | `-1`（無制限） | ❌ | オブジェクト単位のサイズ上限なし。数 GB のダウンロードもまるごとキャッシュ。 |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | 4.5d / 1MB | ❌ | 古く大きいエントリの切り詰めなし。ファイル単位の削除のみ。 |
| `diskCacheWriteoutOnClose`, `maxDiskEntries` | 64KB / 32 | 🔷 | FD 管理 / 書き込みバッチは Go ランタイムの責務。 |
| `diskCacheDirectoryPermissions` / `diskCacheFilePermissions` | `0700` / `0600` | ⚠️ | `0700` / `0600` にハードコード（`MkdirAll`/`OpenFile`）。設定不可。既定値とは一致。 |
| アトミック書き込み（一時ファイルに書いて rename） | — | ⚠️ | `Store` は `O_TRUNC` でインプレース書き込みし、失敗時に `os.Remove`。書き込み途中のクラッシュで短いファイルが残る（後で `Lookup`/`Expire` が「破損」として検出）。Polipo はインプレースの切り詰めを避ける。 |
| `X-Polipo-Location` 衝突ガード | — | ✅ | 実装済み。読み出し時に URL 完全一致を比較。 |

## 12. DNS

| 変数 | 状態 | 備考 |
| :--- | :--: | :--- |
| `dnsUseGethostbyname`, `dnsNameServer[Port]`, `dnsMaxTimeout`, `dnsNegativeTtl`, `dnsGethostbynameTtl`, `dnsQueryIPv6` | 🔷 | 内部リゾルバ全体を Go の `net.Resolver` に置換（ARCHITECTURE.md 記載）。独自 DNS キャッシュなし、クエリ単位の IPv6 優先ノブなし。 |

## 13. ローカル Web インターフェース

| ページ / 変数 | 状態 | 備考 |
| :--- | :--: | :--- |
| `/polipo/` , `/polipo/config` | ✅ | 実装済み。`config` は**読み取り専用**の表。 |
| `disableLocalInterface` | ✅ | 尊重。 |
| Web からの設定**編集** / `disableConfiguration` | ❌ | 未実装（ページは読み取り専用）なのでガードは無意味。 |
| `/polipo/status?`（状態表示 + キャッシュフラッシュ操作） | ❌ | 状態ページなし、実行時キャッシュフラッシュなし。 |
| `/polipo/servers?`（サーバー別 RTT・転送レート・能力） / `disableServersList` | ❌ | サーバー統計を一切収集しない。 |
| `/polipo/index?URL`, `/polipo/recursive-index?` / `disableIndexing` | ❌ | キャッシュインデックスの閲覧なし。 |
| `localDocumentRoot`, `disableProxy` | ❌ | 単なるローカルファイルサーバーとしては動かせない。 |

## 14. ロギング

| 変数 | 状態 | 備考 |
| :--- | :--: | :--- |
| `logLevel` | ⚠️ | パースして保持するが、標準 `log` パッケージは出力をレベルで絞らないため、ほぼ飾り。 |
| `logFile` | ✅ | `log` の出力先を切り替え。追記モード。 |
| `logFilePermissions` | ⚠️ | `0644` にハードコード（Polipo 既定は `0640`）。 |
| `logSyslog`, `logFacility` | ❌ | syslog 出力なし。 |
| `scrubLogs` | ❌ | ログからの個人情報スクラブなし。 |

## 15. デーモンとシグナル

| 機能 | 状態 | 備考 |
| :--- | :--: | :--- |
| `daemonise`, `pidFile` | 🔷 | バックグラウンド化はサービスマネージャ / Windows インストーラ + タスクスケジューラ任せ。 |
| `SIGTERM`, `SIGINT` | ✅ | `server.Close()` でグレースフル終了。 |
| `SIGHUP` | ❌ | 設定リロードなし。 |
| `SIGUSR1`（キャッシュ再検証 + `forbidden` 再読み込み） | ❌ | — |
| `SIGUSR2`（メモリキャッシュ破棄 + `forbidden` 再読み込み） | ❌ | —（メモリキャッシュ自体が無い。forbidden 再読み込みも無い） |

---

## トリアージ用ショートリスト

### A. 正確性 / 安全性 — 公開前に検討すべき

1. ~~**`allowedPorts` が死んだ設定（§3）。**~~ **完了（2026-09-11）:** 転送経路で
   `403` により強制。既定を Polipo の `80-100, 1024-65535` に戻し、`lo-hi` レンジ
   構文もサポート。CONNECT は Polipo 同様 `tunnelAllowedPorts` のみで判定する。
   *悪用面を封鎖: 任意 TCP ポートへのオープンリレーは不可に。*
2. ~~**`socksProxyType` が死んだ設定（§5）。**~~ **完了（2026-09-11）:** 変数は残し、
   `socks5` 以外を設定エラーにした。`socks4a` の暗黙ダウングレードは廃止。SOCKS4/4a
   は設計上非実装（RFC 無し・認証/IPv6 無し・現代のプロキシで不要）。
3. ~~**`Vary` 未対応（§9）。**~~ **完了（2026-09-11）:** 最小安全策を採用 — `Vary`
   が空でない応答は保存も配信もしない（`Accept-Encoding` の特例なし。ロジックと
   ディスク形式は不変）。誤変種バグを封鎖。変種ごとのキャッシュは将来項目。
4. ~~**ヒューリスティック鮮度なし（§8）。**~~ **完了（2026-09-11）:** ヘッダの無い
   `200` は `min(0.1 × Last-Modified 経過時間, 24h)`（`Last-Modified` 無しは 24h
   固定）を得る。Polipo の既定の割合／上限に一致、ハードコード。同じ処理で
   `s-maxage` と不正 `Expires` も正しく扱うようにした。

### B. 機能ギャップ — 「後で移植」か「移植しない」を決め、文書化する

- プロキシ認証 `authCredentials` / `authRealm`（§2） — 現状のアクセス制御は IP のみ。
- `parentAuthCredentials` / `socksAuthCredentials`（§5） — 認証付き上流プロキシを
  使えない。
- `censoredHeaders` / `censorReferer`（§7） — プライバシー用ヘッダ除去。今回の
  Cookie / Authorization ハードニングと地続き。
- 条件付き再検証 / `304`（§8） — 帯域幅の問題。`relaxTransparency` や忠実な
  透過性モデルの前提でもある。
- `relaxTransparency` / `proxyOffline`（§8） — 失敗時 stale 配信・オフラインモード。
- シグナルまたはエンドポイント経由の `forbidden`/`uncachable` 実行時リロード
  （§4, §15）。
- `forbiddenTunnelsFile`（§4） — ホスト単位の CONNECT ブロックリスト。
- ディスクキャッシュのサイズ上限 `maxDiskCacheEntrySize` / 切り詰め（§11） —
  `-x` 実行間の無制限な肥大化。
- Polipo 方式の `Vary` 照合（§9） — A.3 は `Vary` 応答を「キャッシュしない」ことで
  安全にした。follow-up は、エントリに「そのリクエストの Vary 対象ヘッダ値」を
  一緒に保存し（`X-Polipo-Vary` メタヘッダ1個の追加、1 URL 1 ファイルは維持）、
  照合すること。`Vary: Accept-Encoding` 応答が再びキャッシュ可能になる。`cache.go`
  に約 80〜140 行。リスクはヘッダ値の正規化（`Accept-Encoding` はトークン集合比較、
  Go の transport が書き換える「ヘッダ無し」ケース）。URL ごとの複数変種ストアは
  対象外（管理が増え、ディスク形式が非互換になる）。
- キャッシュフラッシュ操作つきの `/polipo/status?`（§13） — 長時間稼働プロキシに
  とって安価な可観測性 + 運用性の向上。

### C. 明確に設計上の割り切り（ARCHITECTURE.md に記載済み。一文添えるだけでよい）

- インメモリキャッシュ、部分インスティレーション、レンジ組み立て、PMM（§10）。
- すべての DNS ノブ（§12）、すべてのコネクションプール / パイプラインノブ（§10）。
- `daemonise` / `pidFile`（§15）。
- `logLevel` の粒度、syslog（§14）。

### D. 些末 / 価値が低い

- `proxyName` / `displayName`、`disableVia` トグル（§7）。
- `forbiddenUrl` / `forbiddenRedirectCode`（§4）。
- `redirector`（§4）。
- `localDocumentRoot` / `disableProxy`、キャッシュインデックスページ（§13）。
- `diskCache*Permissions`、`logFilePermissions` の設定可能化（§11, §14）。
- `proxyOutgoingAddress`、`useTemporarySourceAddress`（§3）。
