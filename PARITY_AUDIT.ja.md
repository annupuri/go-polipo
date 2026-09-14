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
| `authCredentials` | (null) | ✅ | **強制済み（2026-09-12 解決）。** `"user:password"` の単一文字列。空（既定）なら Polipo と同じく認証不要。設定時は `ProxyHandler.isAuthorized` が転送・CONNECT・ローカル `/polipo/*` ページを問わず、他のどの処理よりも先に全リクエストを検査する。認証情報が無い/誤りの場合、通常の転送リクエストと CONNECT には `407`/`Proxy-Authenticate`、go-polipo に（プロキシ経由でなく）直接アクセスしたリクエストには `401`/`WWW-Authenticate`（`Proxy-Authorization` ではなく `Authorization` を検証）を返す — これは Polipo の `urlIsLocal` による分岐で、推測ではなくベンダリングされた C 版の履歴（`auth.c`, `client.c`）から確認した。`parentAuthCredentials` 実装時（2026-09-13）に発見：Polipo の `config.c` は `CONFIG_PASSWORD` 型の変数の実際の値を `-v` や設定ページに一切表示せず、常に `(hidden)` とだけ表示する。`authCredentials` もこの型の一つだが、go-polipo の `Entries()` は平文で表示していた。新規追加した2つのパスワード変数と併せて修正済み。 |
| `authRealm` | (可変) | ✅ | **強制済み（2026-09-12 解決）。** 既定値は `"Polipo"`。`authCredentials` が設定されているときのみ意味を持つ（Polipo も `authCredentials` が非 null なら起動時に同じ既定値を補う）。`Basic realm="..."` チャレンジにそのまま使われる。 |

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
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ✅ | **完了（2026-09-14）:** Polipo の `tunnelIsMatched`（`tunnel.c`）と同じ判定方式で移植した — ホスト名の完全一致（サブドメインには広がらない）と、生の `host:port` に対する正規表現マッチの2本立てで、`forbiddenFile` のサフィックスマッチとは独立（`forbidden.go` の `ForbiddenList.MatchTunnel`）。この行の従来の記載への訂正: go-polipo は元々 CONNECT を「ポートのみ」で判定してはいなかった — `forbidden.go` の一般的なリスト（サフィックスマッチ）は最初の Go 移植時（`proxy.go` の `targetURL`）から CONNECT にも適用されており、これは実は Polipo より広い挙動である。本家 Polipo の `client.c` は CONNECT を `do_tunnel` へ直接ディスパッチし `urlForbidden` を一切呼ばない。この既存の広い挙動は意図的に維持し、Polipo に忠実な新設の `forbiddenTunnelsFile` の上に多重防御として重ねている。 |
| `redirector` / `redirectorRedirectCode` | (未設定) | ❌ | Squid 風の外部リダイレクタ連携なし。 |
| これらファイルの実行時リロード | `SIGUSR1`/`SIGUSR2` | ✅ | **完了（2026-09-14）:** `ProxyHandler.Reload` が両ファイルを再読み込みし、`sync/atomic.Pointer` で原子的に差し替える — 処理中のリクエストが「半分だけ更新されたリスト」を見ることはない。Unix では `SIGUSR1`/`SIGUSR2`（`signal_unix.go`）、任意のプラットフォーム（Windows を含む）では `POST /polipo/reload` から到達可能。Windows には `syscall.SIGUSR1`/`SIGUSR2` 自体が存在しないため（`signal_windows.go` は何もしない）。§15 参照。 |

## 5. 親プロキシ

| 変数 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (未設定) | ✅ | `http.Transport.Proxy` 経由の HTTP 親プロキシ。 |
| `parentAuthCredentials` | (未設定) | ✅ | **実装済み（2026-09-13 解決）。** `"user:password"` 文字列をそのまま（ユーザー名/パスワードに分割せず）base64 エンコードし、`parentProxy` 経由の全リクエストに `Proxy-Authorization: Basic ...` として付与する。推測ではなくベンダリングされた C 版の履歴（`auth.c` の `buildServerAuthHeaders`）そのままの挙動。`ReverseProxy` の `Director` ではなく、それをラップする `http.RoundTripper` 経由で付与している — `Director` はホップバイホップヘッダ（`Proxy-Authorization` を含む）が返却直後に剥がされてしまうため、そこで設定しても実際の通信には乗らない。 |
| `socksParentProxy` | (未設定) | ✅ | `golang.org/x/net/proxy` 経由。 |
| `socksProxyType` | `socks5` | ✅ | **`socks5` のみ受理（2026-09-11 解決）。** `golang.org/x/net/proxy` は SOCKS5 専用。`Config.Set` が `socks4a` やタイプミスを起動時エラーで弾くようにした（従来は保持だけして結局 SOCKS5 で接続）。SOCKS4/4a は意図的に非実装 — RFC が無く、認証も IPv6 も無く、現代の SOCKS プロキシで必要とされない。 |
| `socksAuthCredentials` | (未設定) | ✅ | **実装済み（2026-09-13 解決）。** `"username:password"`（コロン無しなら `"username"` のみ、パスワード無し）を最初の `:` で分割し、`golang.org/x/net/proxy.SOCKS5` の `*Auth` に渡す（RFC 1929 のサブネゴシエーション）。Polipo の `preinitSocks`（`socks.c`）と同じ分割ロジック。go-polipo は SOCKS5 のみ対応するのでSOCKS5の範囲でのみ意味を持つ（Polipo は SOCKS4a でも受け付けていた）。 |

## 6. トンネリング（CONNECT）

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | `handleConnect` で強制。go-polipo 既定は `443, 563` のみで Polipo より狭い。 |
| 双方向ハーフクローズのトンネル | — | ✅ | `tunnel()` が両方向の終了を待つ。CONNECT 上の HTTP/2 に対応。 |
| CONNECT とリクエスト同時実行数制限 | — | ✅ | CONNECT を `maxConcurrentRequests` から意図的に除外（最近の修正）。 |

## 7. HTTP ヘッダの扱いとプライバシー

| 変数 / 機能 | Polipo 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (空) | ✅ | **実装済み（2026-09-13 解決）。** ヘッダ名のリストを、転送リクエスト（`Director`）と受信レスポンス（`cacheResponse`、キャッシュ・クライアントへの返却より前）の両方から除去する — 送受信で別リストではなく、Polipo の単一のヘッダコピーフィルタ（`http_parse.c`）と同じく1つのリストを両方向でチェックする。レスポンスへの適用はフェッチ時に一度きりなので、後のキャッシュヒットは既にクリーンな状態（Polipo の「パース時に一度だけ」というモデルと一致）。 |
| `censorReferer` | `false` | ⚠️ | **実装済み（2026-09-13 解決）。** 推測ではなくベンダリングされた C 版の履歴（`http_parse.c`）から確認した Polipo の三値：`false`（既定）は `Referer` をそのまま転送、`maybe` はリクエスト先と異なるホストを指す場合のみ削除（クロスオリジン時のみ）、`true` は常に削除。1点意図的に変えた箇所がある：クロスオリジン判定に Polipo の `urlSameHost`（`http://` という4文字スキームのみを前提にした ASCII 前置比較で、`https://` の Referer は原理的に絶対に「同一ホスト」と判定されない）をそのまま再現するのではなく `net/url` を使った — go-polipo 版は `https://` でも正しく動く、C ソースの字面より踏み込んだ改善。 |
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
| **条件付き再検証** | — | ⚠️ | **実装済み・上流方向のみ（2026-09-13 解決）。** 期限切れキャッシュエントリが `ETag` および/または `Last-Modified` を持つ場合、`ProxyHandler` の `Director`（`addConditionalHeaders`）が転送リクエストを条件付きにする。`304 Not Modified` が返れば、RFC 9111 §4.3.4 に沿って保存済みエントリへマージする（`reviveFromCache`、`ModifyResponse` 内）— `Cache-Control`・`Content-Location`・`Date`・`ETag`・`Expires`・`Vary` を `304` の値で更新し、キャッシュ済みの本文と他のヘッダはそのまま維持、マージ結果を再度 `Store` することで新しい鮮度期間が即座に有効になる（本文の再取得なし）。`200` が返れば従来どおりエントリを丸ごと置き換える。意図的にスコープを絞った点：go-polipo 自身が**クライアントからの**条件付きリクエストをキャッシュだけで処理する機能（RFC 9111 §4.3.5）は実装していない — クライアントが送ってきた `If-None-Match`/`If-Modified-Since` はそのまま尊重し（go-polipo 自身が合成した値で上書きしない）上流にそのまま転送するだけで、返ってきた応答（本文なしの `304` を含む）はどちらの経路でも同じロジックで処理する。 |
| **ヒューリスティック鮮度**（`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`） | (可変) | ⚠️ | **実装済み・設定不可（2026-09-11 解決）。** `Expires` / `max-age` / `s-maxage` の無い `200` は `isExpired` でヒューリスティック寿命 `min(0.1 × (Date − Last-Modified), 24h)`（`Last-Modified` 無しなら 24h 固定）を得る。`Lookup` と `-x` の両方に効く。**`maxAge` / `maxAgeFraction`：** Polipo は設定変数だったが go-polipo は同じ既定値をハードコード — 運用ポリシーではなく実質 RFC 由来の定数で、緩い値にすると今回直した過剰配信が復活するため（`cache.go` のコメント参照）。**`maxExpiresAge`**（未来の `Expires`/`max-age` をどこまで信用するかの上限）：先送り — `diskCacheUnlinkTime`（32日）が失効を頭打ちにするが、継続アクセスされるエントリには不完全。**`maxNoModifiedAge`**：条件付き再検証の実装中にベンダリングされた C 版の履歴（`object.c`）で確認したところ、これは再検証とは無関係で、`Expires`/`max-age` も `Last-Modified` も無い応答に対するヒューリスティック鮮度の上限だった（`maxNoModifiedAge = 23 * 60` 秒 = 23分）。これにより go-polipo の既存のヒューリスティック鮮度実装（`heuristicMaxLifetime`、2026-09-11 解決）に忠実性のバグがあることが判明した：go-polipo は同じ「`Last-Modified` 無し」のケースに一律24時間を使っている（Polipo の23分より約1300倍も緩い）上に、`Last-Modified` が**ある**場合の `maxAgeFraction` 計算の上限としても同じ24時間を使っているが、Polipo はそちらには `maxAge`（`14 * 24 + 1` 時間 ≈ 約14日、go-polipo の24時間よりずっと緩い）を使っている。この項目の範囲外なので今回は修正していない — ショートリスト参照。 |
| `preciseExpiry` | `false` | 🔷 | 上記が無い以上、該当なし。 |
| `cacheIsShared` | `true` | ⚠️ | トグルは無いが、共有キャッシュのルールは部分的に強制されている。`private` は常にスキップ。認証付き応答は明示的に共有可能でない限りスキップ（ディスクキャッシュのハードニング）。 |
| `relaxTransparency` | `false` | ⚠️ | **実装済み・GET/HEAD のみ（2026-09-13 解決）。** `false`/`maybe`/`true` の三値。ベンダリングされた C 版の履歴（`http.c` の `proxyOffline`/`relaxTransparency` 直前のコメント、`client.c` での使用箇所）から確認した挙動に合わせた。`false`（既定）：上流との通信に失敗すればそのままエラー。`maybe`：上流に一切到達できないとき、`ReverseProxy` の `ErrorHandler` がエラーの代わりに stale なキャッシュを `Warning: 111 ... "Revalidation failed"` 付きで返す。上流に到達できた場合は通常どおり配信・キャッシュする。`true`：`handleForward` がネットワークに一切触れず既存エントリを配信する — 新鮮なら通常経路、stale なら `Warning: 110 ... "Object is stale"` 付き — ので、本当にキャッシュミスのときだけ上流へ到達する。go-polipo には Polipo の「再検証が必要」という独立した状態がない（期限切れエントリは無条件に丸ごと再取得する）ため、Polipo 内部の `validate`/`objectMustRevalidate` の仕組みそのものではなく、観測可能な「失敗時／stale 配信」挙動だけを移植した。CONNECT トンネルは対象外（Polipo でも `tunnel.c` にはどちらの変数も現れず、影響しない）。 |
| `proxyOffline` | `false` | ⚠️ | **実装済み・GET/HEAD のみ（2026-09-13 解決）。** true のとき `handleForward` は転送リクエストで上流に一切ダイヤルしない：キャッシュにあれば新鮮でも stale でもそのまま配信し（stale なら `Warning: 112 ... "Disconnected operation"`）、本当にキャッシュミスなら通信を試みず Polipo と同じ文言 `502 Disconnected operation and object not in cache` を返す。CONNECT トンネルは対象外で、Polipo の `httpServerRequest` のゲート（`server.c`）にも手を入れていない点と一致する。 |

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
| `maxDiskCacheEntrySize` | `-1`（無制限） | ✅ | **完了（2026-09-14）:** `Store`（`cache.go`）で強制。Polipo の `makeDiskEntry`（`diskcache.c`）と同じ判定 — 本文を読む前に宣言済みの `Content-Length` でまず判定し（多くのケースで無駄なバッファリングを避ける）、チャンク転送などで長さが不明/不正確な場合は実際に読んだバイト数でフォールバック判定する。上限超過でもクライアントへの転送は行い、キャッシュのみスキップする。 |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | 4.5d / 1MB | ✅ | **完了（2026-09-14）。意図的に「切り詰め」ではない:** `-x` 実行時、`diskCacheTruncateSize` より大きく `diskCacheTruncateTime` 以上未使用のエントリを、一般的な `diskCacheUnlinkTime` の閾値より前に削除するようにした — 発動条件自体はPolipoの`expireDiskObject`（`diskcache.c`）と同じで、推測ではなくベンダリングされたC版の履歴から確認した。意図的に変えた点: Polipoはファイルの本文をその場で`diskCacheTruncateSize`まで切り詰め、後で部分インスタンス機構で欠けた末尾を再取得する。go-polipoには部分インスタンス機構が無い（§10）ため、その場で切り詰めると保存済みの`Content-Length`がファイルの実際の終端を超えたままになり、後の`Lookup`が「完全である」と偽ったまま途中で途切れた応答をクライアントに返してしまう。代わりに削除することで、「早期に空き容量を確保する」という同じ効果を、壊れた応答を配信するリスク無しに得られる。通常の`-x`削除とは区別して`ExpireStats.RemovedForSize`と専用のログ行で追跡できるようにした — これは下記の実例を見つけた本人からの要望による。 |
| `diskCacheWriteoutOnClose`, `maxDiskEntries` | 64KB / 32 | 🔷 | FD 管理 / 書き込みバッチは Go ランタイムの責務。 |
| `diskCacheDirectoryPermissions` / `diskCacheFilePermissions` | `0700` / `0600` | ⚠️ | `0700` / `0600` にハードコード（`MkdirAll`/`OpenFile`）。設定不可。既定値とは一致。 |
| アトミック書き込み（一時ファイルに書いて rename） | — | ⚠️ | `Store` は `O_TRUNC` でインプレース書き込みし、失敗時に `os.Remove`。書き込み途中のクラッシュで短いファイルが残る（後で `Lookup`/`Expire` が「破損」として検出）。Polipo はインプレースの切り詰めを避ける。 |
| ディスクキャッシュの書き込み/期限切れ失敗時のログ出力 | Polipo は `do_log_error` で記録 | ✅ | **完了（2026-09-14）。上記2行の作業中に発見:** `Store` の失敗分岐（`GetCachePath`、`MkdirAll`、`OpenFile`、`Write`）と `Expire` の削除処理は、これまで全て無言で握り潰されていた（`cache.go` は `log` パッケージすら import していなかった）。今回すべてログ出力するようにし、上記の「サイズ起因の早期削除」も通常の破損/HTTP鮮度切れ/unlink-time削除とは別にログ行を分けた。 |
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
| `SIGUSR1`（インメモリデータの書き出し + `forbidden` 再読み込み） | ✅ | **完了（2026-09-14）:** `forbidden`/`uncachable`（§4）を再読み込みする。Polipo の「書き出し」側（`writeoutObjects`）に相当する処理は無い — go-polipo のキャッシュはディスクのみで、メモリだけに保持されているデータが無いため。 |
| `SIGUSR2`（メモリキャッシュ破棄 + `forbidden` 再読み込み） | ✅ | **完了（2026-09-14）:** `SIGUSR1` と同じ再読み込み。go-polipo には破棄すべき独立したメモリキャッシュが無い（§10）ため、Polipo とは異なりこの2つのシグナルは同じ動作になる。 |

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

- ~~プロキシ認証 `authCredentials` / `authRealm`（§2）。~~ **完了（2026-09-12）:**
  移植した — `ProxyHandler.isAuthorized` が単一の `"user:password"` 認証情報と、
  Polipo の `401`/`407` の使い分け（`urlIsLocal`）を強制する。実装は推測ではなく
  ベンダリングされた C 版の履歴から確認した。ついでに見つけた関連バグも修正：
  `cache.go` の認証付き応答のキャッシュ安全性ルール（§8・ディスクキャッシュの
  ハードニング）が `Proxy-Authorization` を持つリクエストもキャッシュ対象外に
  していたのは誤りだった — Polipo 自身の `CACHE_AUTHORIZATION` フラグは
  `Authorization` にしか立たない（`http_parse.c`）。`Proxy-Authorization` は
  ホップバイホップでオリジンには届かないため。直さないままこの機能を有効化
  すると、正規のリクエストすべてが `Proxy-Authorization` を持つことになり、
  ディスクキャッシュが黙って機能しなくなるところだった。
- ~~`parentAuthCredentials` / `socksAuthCredentials`（§5）。~~ **完了
  （2026-09-13）:** 両方とも移植した — 推測ではなく、ベンダリングされた C 版の
  履歴（`auth.c` の `buildServerAuthHeaders`、`socks.c` の `preinitSocks`）から
  確認した。`parentAuthCredentials` は（ユーザー名/パスワードに分割せず）文字列
  全体を base64 エンコードし、`parentProxy` 経由の全リクエストに
  `Proxy-Authorization` として付与する — `ReverseProxy` の `Director` ではなく、
  それをラップする `http.RoundTripper` が必要だった（`Director` が設定した
  ホップバイホップヘッダは `Director` の返却直後に剥がされ、実際の通信には
  乗らないため）。`socksAuthCredentials` は最初の `:` で分割し、
  `golang.org/x/net/proxy.SOCKS5` の RFC 1929 認証に渡す。ついでに発見・修正:
  `authCredentials` が `-v` / 設定ページで平文表示されていた — この3変数が
  共有する Polipo の `CONFIG_PASSWORD` 型は常に `(hidden)` としか表示しない
  仕様だったので、3つとも揃えた。
- ~~`censoredHeaders` / `censorReferer`（§7）。~~ **完了（2026-09-13）:** 両方とも
  移植した — 推測ではなく、ベンダリングされた C 版の履歴（`http_parse.c`）から
  確認した。`censoredHeaders` はヘッダ名のリストで、転送リクエストと受信
  レスポンスの両方から除去する（送受信で別リストにせず、Polipo の単一の
  ヘッダコピーフィルタと同じ1リスト方式）。`censorReferer` は Polipo の
  `false`/`maybe`/`true` 三値（そのまま転送／クロスオリジン時のみ削除／常に
  削除）。1点意図的に変えた箇所：クロスオリジン判定は Polipo の
  `urlSameHost`（`http://` のみを前提とした ASCII 前置比較で、`https://` の
  Referer は原理的に「同一ホスト」と判定されない）をそのまま再現せず
  `net/url` を使った — go-polipo 版は `https://` でも正しく動く。
- ~~条件付き再検証 / `304`（§8）。~~ **完了（2026-09-13）:** 上流方向の半分を
  移植した — 推測ではなく、ベンダリングされた C 版の履歴（`object.c`、
  `http_parse.c`）から確認した。バリデータを持つ stale なエントリはまるごと
  再取得ではなく条件付き GET になり、`304` が返れば（RFC 9111 §4.3.4）
  新規ダウンロードの代わりに保存済みエントリへマージする。意図的に見送った点：
  **クライアントからの**条件付きリクエストを上流に一切問い合わせずキャッシュ
  だけで処理する機能（RFC 9111 §4.3.5）— クライアント自身のバリデータは
  ショートカットせずそのまま上流に転送する。今回発見したが対応していない点
  （このアイテムの範囲外）：`maxNoModifiedAge` の意味を確認する過程で、
  既存のヒューリスティック鮮度の実装が一律24時間の上限を使っているが、
  Polipo は`Last-Modified` がある場合は `maxAge`（約14日）、無い場合は
  `maxNoModifiedAge`（23分）という全く異なる2つの値を使い分けていることが
  判明した — §8のヒューリスティック鮮度の行を参照。
- ~~`relaxTransparency` / `proxyOffline`（§8）。~~ **完了（2026-09-13）:** 両方とも
  観測可能な挙動を移植した — 推測ではなく、ベンダリングされた C 版の履歴
  （`http.c` のコメントブロック、`client.c` での両変数の使用箇所）から確認した。
  `relaxTransparency` は `false`/`maybe`/`true` の三値：`maybe` は上流に到達
  できないとき stale なキャッシュを（`Warning: 111` 付きで）代わりに返し、
  `true` はネットワークを一切試みず既存エントリ（stale でも）をそのまま返す。
  `proxyOffline` は上流に一切ダイヤルしない — キャッシュヒットか `502` のどちらか。
  GET/HEAD（go-polipo のキャッシュが扱う唯一のメソッド）と転送リクエストのみが
  対象（CONNECT は対象外、Polipo と同様）。見送った部分：Polipo 内部の
  `validate`/`objectMustRevalidate` の区別そのもの — 条件付き再検証（上記の
  `304` 項目）が無い go-polipo にはこれに相当する状態がない。
- ~~シグナルまたはエンドポイント経由の `forbidden`/`uncachable` 実行時リロード
  （§4, §15）。~~ **完了（2026-09-14）:** `ProxyHandler.Reload` が両ファイルを
  再読み込みし、原子的に差し替える。Unix では `SIGUSR1`/`SIGUSR2`
  （`signal_unix.go`）から起動でき、Polipo の「シグナルで `initForbidden()`」
  という挙動に対応する — go-polipo には `SIGUSR2` が破棄すべき独立した
  メモリキャッシュが無いため、両シグナルの動作は同じにした。Windows には
  `SIGUSR1`/`SIGUSR2` 自体が存在しない（`syscall` パッケージに無い）ため、
  `POST /polipo/reload` でこのプラットフォームをカバーする — こちらは
  どの環境でも使える。これは Polipo の `/polipo/status?init-forbidden`
  アクションの移植ではなく go-polipo 独自の新設エンドポイントである。
  本家の同アクションは RTT 統計・キャッシュフラッシュ・`304` 破棄なども
  持つ status ページの一部であり、go-polipo にはそのページ自体が無いため
  （§13）、パス／形式をそのまま踏襲するのは適切でないと判断した。
- ~~`forbiddenTunnelsFile`（§4） — ホスト単位の CONNECT ブロックリスト。~~
  **完了（2026-09-14）:** 推測ではなく、ベンダリングされた C 版の履歴
  （`tunnel.c` の `tunnelIsMatched`）から確認して移植した。ドメイン欄は
  ホスト名の完全一致（サブドメインには広がらない）、正規表現欄は生の
  `host:port` に対して照合する — どちらも `forbiddenFile` のサフィックス
  マッチとは異なるため、`Match` をそのまま流用するのは誤りだった。作業中に
  判明したこと: 一般的な `forbidden` リストは（`targetURL` 経由で）既に
  CONNECT にも適用されており、本監査が「CONNECT はポートのみ」と記録して
  いたのは今回の変更以前から実態と食い違っていた。本家 Polipo は CONNECT に
  対して `forbiddenFile` を一切見ない（`client.c` が `do_tunnel` へ直接
  ディスパッチする）。go-polipo のこの広い挙動は最初の移植時からの既存仕様
  であり、意図的な追加保護として維持し、新設の `forbiddenTunnelsFile` で
  置き換えるのではなく併用することにした。
- ~~ディスクキャッシュのサイズ上限 `maxDiskCacheEntrySize` / 切り詰め（§11） —
  `-x` 実行間の無制限な肥大化。~~ **完了（2026-09-14）:**
  `maxDiskCacheEntrySize` は `Store` への単純な入口ゲートとして移植した。
  `diskCacheTruncateTime`/`diskCacheTruncateSize` は Polipo のインプレース
  切り詰めではなく早期**削除**として移植した — 部分インスタンス機構（§10）
  無しに切り詰めるとエントリが壊れるため、これは手抜きではなく意図的な
  置き換えである。作業中に併せて修正: `Store`/`Expire` のどの失敗経路も
  ログに何も出していなかった（`cache.go` は `log` パッケージすら import
  していなかった） — この項目を担当していた本人が「異常系はログに残る
  ようになっているか」と尋ねたことで発覚し、実際には残っていなかった。
  削除ではなく切り詰めを選ばなかった判断を裏付ける具体的な根拠: この会話中に
  旧 Polipo 時代（約10年前）の実際のキャッシュファイルを復元してみたところ、
  まさにこのシナリオそのものだった — `Content-Length: 218811050` と宣言
  されているのに、ディスク上には約111MBしか実在しない。つまり Polipo 自身の
  切り詰め処理（あるいは元々の取得が不完全だったこと）により、宣言サイズ
  より小さく縮んだインスタンスだった。
- Polipo 方式の `Vary` 照合（§9） — A.3 は `Vary` 応答を「キャッシュしない」ことで
  安全にした。follow-up は、エントリに「そのリクエストの Vary 対象ヘッダ値」を
  一緒に保存し（`X-Polipo-Vary` メタヘッダ1個の追加、1 URL 1 ファイルは維持）、
  照合すること。`Vary: Accept-Encoding` 応答が再びキャッシュ可能になる。`cache.go`
  に約 80〜140 行。リスクはヘッダ値の正規化（`Accept-Encoding` はトークン集合比較、
  Go の transport が書き換える「ヘッダ無し」ケース）。URL ごとの複数変種ストアは
  対象外（管理が増え、ディスク形式が非互換になる）。
- キャッシュフラッシュ操作つきの `/polipo/status?`（§13） — 長時間稼働プロキシに
  とって安価な可観測性 + 運用性の向上。
- インメモリオブジェクトキャッシュ（§10）— 当初は ARCHITECTURE.md の通り
  「Go の GC / 標準ライブラリに任せれば手製アロケータ（`atom.c`/`chunk.c`/
  `object.c`）は不要」という設計上の割り切りとして C 枠に分類していたが、
  それは SSD 前提の判断であり、低速な HDD 上で運用するマシンには今でも
  効くはずだとのユーザー指摘（2026-09-14）を受けて B 枠に切り出した。
  ユーザーが挙げた実装方針の叩き台: (1) 自前 LRU ではなく
  `github.com/hashicorp/golang-lru` 等の既製ライブラリでサイズ上限を管理、
  (2) `CacheManager.Store` がディスクを更新するたびにメモリ側のエントリも
  同時に更新／無効化する同期処理を入れ、ディスクとメモリの不整合
  （本回答で挙げた懸念点 (2)）を防ぐ、(3) ロックはデッドロックさえ避ければ
  実運用上はロック待ちがボトルネックになることは稀、という想定。現時点では
  未着手 — ディスクキャッシュのみで運用継続を確認済み。
- 部分インスタンス化 / レンジ組み立て（§10）— 元は設計上決着済みの C 枠
  だったが、`diskCacheTruncateTime`/`diskCacheTruncateSize` を切り詰めでは
  なく削除にした理由（上の項目参照）を説明する過程で再浮上した
  （2026-09-14）。go-polipo が安全に切り詰められない根本原因は、まさに
  この機能が無いことそのものだからである。ユーザーから「大きなキャッシュ
  エントリを複数ファイルに分割したらどうか」との提案があったが、検討の
  結果、Polipo 自身の方式 ── 1ファイル + 「どこまで実体があるか」を示す
  バイトオフセット（`X-Polipo-Body-Offset`）── を超えるものにはならない
  という結論になった。この方式はチャンク目録や読み出し時の結合処理が不要
  な分、複数ファイル方式より単純である。どちらの構成を選んでも本質的な
  難しさは変わらず、クライアントの `Range` リクエストを解釈し、キャッシュ
  済みの部分を返し、欠けている範囲だけを上流から取得して埋める、という
  一連の処理が必要になる。未着手。PMM（1回の取得を複数の上流接続に分割
  する仕組み）は関連するが別の機構であり、素朴なレンジ配信を実現する上
  では不要。

### C. 明確に設計上の割り切り（ARCHITECTURE.md に記載済み。一文添えるだけでよい）

- PMM（§10）。
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
