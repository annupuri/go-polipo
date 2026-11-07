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
**呼び方について:** ビルド後のバイナリ名はどちらも `polipo` で紛らわしいため、
以下の表では「Polipo」「go-polipo」ではなく、比較する箇所ではオリジナルのC実装
を「v1.1.x」、この書き直し版を「v2.0.x」と表記する。

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

| 変数 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `proxyAddress` | `127.0.0.1` | ✅ | |
| `proxyPort` | `8123` | ✅ | |
| `allowedClients` | (なし) | ✅ | 素の IP、CIDR、`all`/`*`。実際の TCP ピアアドレスで判定。go-polipo の既定は `127.0.0.1, ::1`。Polipo の既定（未設定）はすべてのクライアントを受け入れたので、`proxyAddress` を変えるだけでオープンプロキシになった。**2026-11-06 から**、ポートに届く人なら誰でもプロキシを使える状態、つまり、ループバック以外で待ち受け、`allowedClients` がすべてのアドレスを認め（`all`、`*`、`0.0.0.0/0`、`::/0`）、`authCredentials` が未設定のときは、起動時に警告をログに出す（`Config.IsOpenProxy`）。信頼できるネットワークでは意図した設定のこともあるので、警告にとどめる。 |
| `authCredentials` | (null) | ✅ | **強制済み（2026-09-12 解決）。** `"user:password"` の単一文字列。空（既定）なら Polipo と同じく認証不要。設定時は `ProxyHandler.isAuthorized` が転送・CONNECT・ローカル `/polipo/*` ページを問わず、他のどの処理よりも先に全リクエストを検査する。認証情報が無い/誤りの場合、通常の転送リクエストと CONNECT には `407`/`Proxy-Authenticate`、go-polipo に（プロキシ経由でなく）直接アクセスしたリクエストには `401`/`WWW-Authenticate`（`Proxy-Authorization` ではなく `Authorization` を検証）を返す — これは Polipo の `urlIsLocal` による分岐で、推測ではなくベンダリングされた C 版の履歴（`auth.c`, `client.c`）から確認した。`parentAuthCredentials` 実装時（2026-09-13）に発見：Polipo の `config.c` は `CONFIG_PASSWORD` 型の変数の実際の値を `-v` や設定ページに一切表示せず、常に `(hidden)` とだけ表示する。`authCredentials` もこの型の一つだが、go-polipo の `Entries()` は平文で表示していた。新規追加した2つのパスワード変数と併せて修正済み。 |
| `clientTimeout` | `120s` | ✅ | **実装済み（2026-11-03）。** クライアントがリクエストヘッダを送り終えるまでの時間（`ReadHeaderTimeout`）と、リクエストの合間に接続を遊ばせておける時間（`IdleTimeout`）の上限。`0` で無効。リクエストやレスポンスの本文には適用しない（`net/http` では本文全体の上限しか設定できず、Polipo の「I/O ごとの無通信タイマー」に当たるものが無い）。CONNECT トンネルにも適用しない（Polipo もトンネルにはタイムアウトを設けていなかった）。Polipo は、オリジンを待っている間にクライアントを切らないよう `clientTimeout` を `serverTimeout` より大きく補正していたが、go-polipo はその待ちの間にクライアント側のタイマーが動かないので、この補正は要らない。以前はこの設定が無く、クライアントはリクエストを送らないまま接続をいつまでも開けておけた。 |
| `authRealm` | (可変) | ✅ | **強制済み（2026-09-12 解決）。** 既定値は `"Polipo"`。`authCredentials` が設定されているときのみ意味を持つ（Polipo も `authCredentials` が非 null なら起動時に同じ既定値を補う）。`Basic realm="..."` チャレンジにそのまま使われる。 |

## 3. 外向き接続

| 変数 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `serverTimeout` / `serverIdleTimeout` | `90s` / `45s` | ✅ | **実装済み（2026-11-03）。** `serverTimeout` は、オリジン・SOCKS・親プロキシへの接続（CONNECT トンネルの接続も含む）、レスポンスヘッダの待ち（`ResponseHeaderTimeout`）、やり取りの途中の無通信（`idleTimeoutConn`。Polipo の `server.c` と同じく、読み書きのたびに期限を延ばす）の上限。時間切れは Polipo と同じく `504`、それ以外の上流の失敗はこれまでどおり `502`。確立した CONNECT トンネルには、Polipo と同じくタイムアウトを設けない（`tunnelIdleTimeout` を設定した場合を除く。§6）。`serverIdleTimeout` は、使っていないオリジンへの接続を再利用のために残しておく時間（`IdleConnTimeout`。以前は 90s 固定だった）。どちらも `0` で無効。以前はこの設定が無く、リクエストを受け付けたまま止まったオリジンが、クライアントが待ち続ける限り `maxConcurrentRequests` の枠を占有していた。 |
| `forbidLocalDestinations` / `forbidPrivateDestinations` | —（該当する検査なし） | ✅ | **追加（2026-11-06）。go-polipo 独自。既定は `true` / `false`。** Polipo は、名前を解決した結果がどのアドレスでも中継していた。そのため、`allowedPorts` と `tunnelAllowedPorts` 以外に歯止めがなく、Web ページが 127.0.0.1 に解決される自前のホスト名（DNS リバインディング）を使うと、このマシンだけで待ち受けているサービスにプロキシ経由で届いてしまった。ブラウザ自身はページをそうしたサービスから遠ざけているのに、それを迂回できた。`forbidLocalDestinations` は、ループバック、未指定アドレス（`0.0.0.0/8`、`::`）、リンクローカル（`169.254.0.0/16`。クラウドのメタデータのアドレスを含む。と `fe80::/10`）、マルチキャストを拒否する。`forbidPrivateDestinations` は、RFC 1918、`fc00::/7`、`100.64.0.0/10` も拒否する。ルーターや LAN 内のマシンへの接続は普通の使い方なので、既定では無効。判定は `net.Dialer` の `Control` 関数で行うので、名前解決した後に実際に接続する各アドレスを見る。拒否するアドレスと許可するアドレスの両方を持つ名前は、許可する方に接続する。拒否した転送や CONNECT には、名前の解決結果を含まない `403` を返し、状態表示の「Denied by policy」に数え、forbidden としてログに残す。`parentProxy`（転送の場合）や `socksParentProxy` が接続するときは、プロキシ自身がオリジンに接続するわけではないので判定しない。 |
| `proxyOutgoingAddress` | OS 既定 | ❌ | 上流ソケットの送信元アドレスを指定できない（マルチホーム環境）。 |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 の一時 IPv6 送信元選択 — OS 任せ。 |
| `allowedPorts` | `80-100, 1024-65535` | ✅ | **強制済み（2026-09-11 解決）。** `handleForward` が対象ポートを求め（`requestPort`）、`AllowedPorts` に無ければ（`PortSet.Contains`）キャッシュ参照や上流ダイヤルの前に `403` で拒否する。既定は Polipo と同じ（`80-100, 1024-65535`）に戻したので、80–100 以外の特権ポート（25, 110, 119, 143, …）は拒否される。`parsePortSet` は Polipo の `lo-hi` レンジ構文（`-100`・`1024-` の開区間も）を受け付ける。CONNECT は Polipo 同様 `tunnelAllowedPorts` のみで判定し、2 つのリストは併用しない。 |

## 4. URL フィルタとリダイレクト

| 変数 / 機能 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | ドメインサフィックス + POSIX 風正規表現 → `403`（`forbidden.go`）。Polipo と同じく、パスにはディレクトリも指定でき、その場合は配下のファイルをすべて（名前順に）読む。Polipo と違い、ディレクトリ内のディレクトリへのシンボリックリンクはたどらない。`uncachableFile` と `forbiddenTunnelsFile` も同様。**2026-11-08 修正:** 以前はディレクトリを指定すると警告を出すだけで読み込みに失敗し、何もブロックされなかった。 |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | 同じマッチャ。該当 URL は転送するがキャッシュしない。 |
| `forbiddenUrl` | (null) | ❌ | ブロックしたリクエストを案内ページへリダイレクトできず、常に素の `403`。 |
| `forbiddenRedirectCode` | `302` | ❌ | 同上。 |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ✅ | **完了（2026-09-14）:** Polipo の `tunnelIsMatched`（`tunnel.c`）と同じ判定方式で移植した — ホスト名の完全一致（サブドメインには広がらない）と、生の `host:port` に対する正規表現マッチの2本立てで、`forbiddenFile` のサフィックスマッチとは独立（`forbidden.go` の `ForbiddenList.MatchTunnel`）。この行の従来の記載への訂正: go-polipo は元々 CONNECT を「ポートのみ」で判定してはいなかった — `forbidden.go` の一般的なリスト（サフィックスマッチ）は最初の Go 移植時（`proxy.go` の `targetURL`）から CONNECT にも適用されており、これは実は Polipo より広い挙動である。本家 Polipo の `client.c` は CONNECT を `do_tunnel` へ直接ディスパッチし `urlForbidden` を一切呼ばない。この既存の広い挙動は意図的に維持し、Polipo に忠実な新設の `forbiddenTunnelsFile` の上に多重防御として重ねている。 |
| `redirector` / `redirectorRedirectCode` | (未設定) | ❌ | Squid 風の外部リダイレクタ連携なし。 |
| これらファイルの実行時リロード | `SIGUSR1`/`SIGUSR2` | ✅ | **完了（2026-09-14）:** `ProxyHandler.Reload` が両ファイルを再読み込みし、`sync/atomic.Pointer` で原子的に差し替える — 処理中のリクエストが「半分だけ更新されたリスト」を見ることはない。Unix では `SIGUSR1`/`SIGUSR2`（`signal_unix.go`）、任意のプラットフォーム（Windows を含む）では `POST /polipo/reload` から到達可能。Windows には `syscall.SIGUSR1`/`SIGUSR2` 自体が存在しないため（`signal_windows.go` は何もしない）。§15 参照。 |

## 5. 親プロキシ

| 変数 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (未設定) | ✅ | `http.Transport.Proxy` 経由の HTTP 親プロキシ。Polipo は `host:port` だけを受け付けたが、Go の Transport は `user:password@host:port` も受け付けて、その認証情報を送る。そのため 2026-11-06 から、このような値（と `socksParentProxy`）のパスワードは、`-v`、`/polipo/config`、ログで `xxxxx` と表示する（`redactProxyURL`）。以前は書いたとおりに表示していた。**CONNECT も親を経由する（2026-11-06 修正）。** Polipo の `tunnel.c` と同じく、親に `CONNECT host:port` を `parentAuthCredentials`（または `parentProxy` に書いた認証情報）付きで送る。親が `https://` なら TLS で、`socksParentProxy` も設定されていればそれを経由する。接続先の名前は親が解決するので、`forbidLocalDestinations` は適用しない。親が 2xx 以外を返したら、クライアントには `502` を返し、CONNECT の失敗として数える。Polipo は親の応答をそのまま渡していたが、親からの `407` は、クライアントが渡しようのない認証情報を求めることになるため。クライアントが CONNECT の直後に送ったバイトは、親が受け入れた後に転送する（Polipo は「Pipelined connect to parent proxy not implemented」として断っていた）。以前は CONNECT が `parentProxy` を無視して接続先に直接接続していたので、親を経由しないと外に出られない環境では HTTPS がつながらなかった。 |
| `parentAuthCredentials` | (未設定) | ✅ | **実装済み（2026-09-13 解決）。** `"user:password"` 文字列をそのまま（ユーザー名/パスワードに分割せず）base64 エンコードし、`parentProxy` 経由の全リクエストに `Proxy-Authorization: Basic ...` として付与する。推測ではなくベンダリングされた C 版の履歴（`auth.c` の `buildServerAuthHeaders`）そのままの挙動。`ReverseProxy` の `Director` ではなく、それをラップする `http.RoundTripper` 経由で付与している — `Director` はホップバイホップヘッダ（`Proxy-Authorization` を含む）が返却直後に剥がされてしまうため、そこで設定しても実際の通信には乗らない。 |
| `socksParentProxy` | (未設定) | ✅ | `golang.org/x/net/proxy` 経由。 |
| `socksProxyType` | `socks5` | ✅ | **`socks5` のみ受理（2026-09-11 解決）。** `golang.org/x/net/proxy` は SOCKS5 専用。`Config.Set` が `socks4a` やタイプミスを起動時エラーで弾くようにした（従来は保持だけして結局 SOCKS5 で接続）。SOCKS4/4a は意図的に非実装 — RFC が無く、認証も IPv6 も無く、現代の SOCKS プロキシで必要とされない。 |
| `socksAuthCredentials` | (未設定) | ✅ | **実装済み（2026-09-13 解決）。** `"username:password"`（コロン無しなら `"username"` のみ、パスワード無し）を最初の `:` で分割し、`golang.org/x/net/proxy.SOCKS5` の `*Auth` に渡す（RFC 1929 のサブネゴシエーション）。Polipo の `preinitSocks`（`socks.c`）と同じ分割ロジック。go-polipo は SOCKS5 のみ対応するのでSOCKS5の範囲でのみ意味を持つ（Polipo は SOCKS4a でも受け付けていた）。 |

## 6. トンネリング（CONNECT）

| 変数 / 機能 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | `handleConnect` で強制。**既定を Polipo と同じにした（2026-10-02 解決）:** `22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418`。以前は `443, 563` のみで Polipo より狭く、例えばポート 80 への CONNECT が拒否されていた。 |
| CONNECT のヘッダーの直後に送られたバイト | 転送する | ✅ | **修正（2026-11-06）。** クライアントは、`200` を待たずにトンネルの最初のバイト（TLS の ClientHello など）を送ってくることがある。そのときサーバーは、ヘッダーの続きとしてそのバイトを読み込み済みになっている。go-polipo は以前、接続を引き取るときの読み込みバッファごとこのバイトを捨てていたので、そうしたトンネルは止まってしまった。今はトンネルを始める前に、まずこのバイトを接続先に送る。Polipo の `makeTunnel` が、バッファに残っていた分を同じように扱っていた。トンネル自体は、これまでどおり生の接続どうしでコピーする。 |
| `tunnelIdleTimeout` | —（上限なし） | ✅ | **追加（2026-11-06）。go-polipo 独自。既定は `0`（無効）。** 確立したトンネルの両方向とも、この時間1バイトも読まなければトンネルを閉じ、`LogInfo` でログに残す。相手が落ちた接続は TCP keepalive がすでに検出する。この設定が対象にするのは、生きているのに黙っている相手と、データを受け取らなくなった相手（このとき反対方向は、新たに読むものがないまま書き込みで止まる）。IMAP IDLE は29分ごとにしかコマンドを出し直さず（RFC 2177）、放置した SSH は何も送らないことがあるなど、長く黙るのが正当なトンネルもあるので、既定は Polipo と同じく無効。読み込みを監視するために接続を包むので、カーネルの splice が使えなくなる。そのため、設定したときだけ監視する。 |
| 双方向ハーフクローズのトンネル | — | ✅ | `tunnel()` が両方向の終了を待つ。CONNECT 上の HTTP/2 に対応。 |
| CONNECT とリクエスト同時実行数制限 | — | ✅ | CONNECT を `maxConcurrentRequests` から意図的に除外（最近の修正）。 |
| `maxConcurrentRequests` の枠を待つ間にクライアントが去った場合 | — | ✅ | **修正（2026-11-06）。** `maxConcurrentRequests` は go-polipo 独自の全体の上限で、Polipo には無い。枠を待っているリクエストは、クライアントが接続を切るとすぐに待つのをやめる（リクエストのコンテキストが取り消される）。以前は、枠が空くまで接続とゴルーチンを持ち続け、その後 Transport に進んで、取り消し済みのコンテキストのために失敗し、オリジンのエラーとして数えてログにも残していた。 |

## 7. HTTP ヘッダの扱いとプライバシー

| 変数 / 機能 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (空) | ✅ | **実装済み（2026-09-13 解決）。** ヘッダ名のリストを、転送リクエスト（`Director`）と受信レスポンス（`cacheResponse`、キャッシュ・クライアントへの返却より前）の両方から除去する — 送受信で別リストではなく、Polipo の単一のヘッダコピーフィルタ（`http_parse.c`）と同じく1つのリストを両方向でチェックする。レスポンスへの適用はフェッチ時に一度きりなので、後のキャッシュヒットは既にクリーンな状態（Polipo の「パース時に一度だけ」というモデルと一致）。 |
| `censorReferer` | `false` | ⚠️ | **実装済み（2026-09-13 解決）。** 推測ではなくベンダリングされた C 版の履歴（`http_parse.c`）から確認した Polipo の三値：`false`（既定）は `Referer` をそのまま転送、`maybe` はリクエスト先と異なるホストを指す場合のみ削除（クロスオリジン時のみ）、`true` は常に削除。1点意図的に変えた箇所がある：クロスオリジン判定に Polipo の `urlSameHost`（`http://` という4文字スキームのみを前提にした ASCII 前置比較で、`https://` の Referer は原理的に絶対に「同一ホスト」と判定されない）をそのまま再現するのではなく `net/url` を使った — go-polipo 版は `https://` でも正しく動く、C ソースの字面より踏み込んだ改善。 |
| `disableVia` | `true`（Via なし） | ✅ | **実装済み（2026-11-03）。既定は意図的に `false`。** RFC 9110 §7.6.3 の必須要件どおり、転送するすべてのリクエストとレスポンスの `Via` に自分を加える（Polipo の既定では付けなかった）。`disableVia = true` で付けなくなる。名前には Polipo の `proxyName`（ホスト名）ではなく、`go-polipo-` に起動時に決めるランダムな文字列を続けた仮名を使う。マシン名をすべてのサイトに知らせないためで、RFC も仮名を認めている。転送しようとするリクエストの `Via` にこのプロセスの名前が既にあればループ（たとえば自分に戻ってくる `parentProxy`）とみなし、Polipo（`checkVia`）と同じく `504 Proxy loop detected` を返す。名前はプロセスごとに異なるので、go-polipo を2台つないだ構成をループと誤認しない。Polipo はレスポンスの `Via` も調べていたが、ループは必ずまずリクエストとして現れるので、go-polipo はリクエストだけを調べる。キャッシュから返すレスポンスには、最初に中継したときの `Via` が付く。自分の `Via` を持ってきた `304` は、保存済みの `Via` を置き換える。CONNECT トンネルには `Via` を付けない。以前は `Via` を付けず、付ける手段もループ検出も無かった。 |
| オリジンに接続できないときのエラー本文 | `Connect to host:port failed: <OS のエラー>` | ✅ | **変更（2026-11-06）。** `502`/`504` の本文はブラウザが表示し、リクエストを出したページも自分のオリジンのものとして読める。go-polipo は以前、Go の生のエラー文を送っていた。そこには、ホスト名を解決した結果のアドレス、ポート、DNS サーバー、接続を拒否されたかどうかが含まれる。そのためページが自前のホスト名を LAN のアドレスに向けると、どのマシンのどのポートが応答するかを調べる手段になった。本文には失敗の種類だけを書くようにした。時間切れなら `Gateway Timeout`、ホスト名が存在しなければ `the host name was not found`（打ち間違いのことが多く、ページが知らないことは何も明かさない）、それ以外は一般的な `Bad Gateway`。詳しいエラーはこれまでどおりログに残す（`upstreamErrorReply`）。Polipo のページは、URL のホスト名とポート、OS のエラーを示していたが、解決後のアドレスや DNS サーバーは示していなかった。 |
| `proxyName` / `displayName` | ホスト名 / `Polipo` | ❌ | プロキシ識別名を設定できない（`Via` にはプロセスごとの仮名を使う。`disableVia` を参照）。エラー本文は Go の平文 `http.Error`。 |
| `X-Forwarded-For` | — | ✅ | 明示的に nil 化し、クライアント IP を転送しない（`Director`）。 |
| `Date` が無い応答 | — | ✅ | **実装済み（2026-11-03）。** 上流から `Date` の無い応答が来たら、受け取った時刻を `Date` として付けてから保存・転送する（`cacheResponse`。RFC 9110 §6.6.1）。Polipo と同じ（`server.c`）。以前は `Date` 無しのまま保存しており、`Date` の無い `304` では保存済みの古い `Date` が残るため、再検証したばかりのエントリがすぐ期限切れ扱いになっていた。 |
| `Max-Forwards`（TRACE、OPTIONS） | — | ✅ | **実装済み（2026-11-03）。** `Max-Forwards: 0` の `TRACE` と `OPTIONS` には、go-polipo が最終的な受け手として自分で答え、1以上なら1減らして転送する（RFC 9110 §7.6.2。`answerMaxForwards`）。`OPTIONS` には空の `200` を返す。`TRACE` には、求められているリクエストのこだまではなく `405`（`Allow: OPTIONS` 付き）を返す。こだまを返すと、go-polipo に届いたヘッダをそのまま返してしまうため。Polipo は `Max-Forwards` を処理せず、`TRACE` には知らないメソッドと同じく `501` を返していた。go-polipo はどのメソッドも転送する。 |
| ホップバイホップヘッダの除去 | — | ✅ | `httputil.ReverseProxy` が RFC 7230 に従い処理。 |
| 内容を変えずに中継 | — | ✅ | **2026-11-03 修正。** 上流用の `Transport` に `DisableCompression` を設定し、`Accept-Encoding` と `Content-Encoding` はクライアントとオリジンが送ったとおりに通す（RFC 9110 §7.7）。Polipo と同じ。以前は無効にしておらず、`Accept-Encoding` を付けないクライアントのリクエストでは、`Transport` が自分で gzip を要求して応答を展開していた。そのため gzip 版の `ETag` で非圧縮の本文を保存していたほか、`Content-Encoding: gzip` なのに展開できない応答は、本文が空のままクライアントに届いていた。 |
| `alwaysAddNoTransform` | `false` | ❌ | 些末。 |
| `laxHttpParser`, `bigBufferSize`, `expectContinue` | — | 🔷 | パースは `net/http` の責務。`ExpectContinueTimeout` はトランスポート内で 1s 固定。 |

## 8. キャッシュ — 鮮度と検証

| 変数 / 機能 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | `isExpired` で尊重。`s-maxage` も尊重し、共有キャッシュである本実装では優先。不正な `Expires`（例 `0`）は失効済み扱い（RFC 9111 §5.3）。`max-age`/`s-maxage` があれば `Expires` は見ない（RFC 9111 §5.3。以前はどちらか一方が切れれば期限切れとしていた）。`Expires` による寿命は、手元の時計とではなく `Date` との差で測る（2026-11-03 修正）。 |
| `Age` | — | ✅ | **実装済み（2026-11-03）。** キャッシュから返すレスポンスには（新鮮なものでも、`relaxTransparency`/`proxyOffline` で返す期限切れのものでも）、現在の年齢を `Age` で付ける。RFC 9111 §4 の必須要件で、Polipo も付けていた（`client.c`。Polipo は1秒未満なら省いたが、go-polipo は `Age: 0` を付ける）。上流から来た `Age` はレスポンスの現在の年齢に含める（RFC 9111 §4.2.3）。`Store` がレスポンスの生成時刻（`Date` と「リクエスト時刻から `Age` を引いた時刻」の早いほう。`responseBirth`）を記録し、そこからの経過時間で鮮度を判断する。ディスクではこの時刻を `X-Polipo-Date` に書く。Polipo もここに同じもの（`server.c` の `object->age`）を保存していた。`304` を受けたら、保存済みの `Age` は `304` の `Age` で置き換え、無ければ消す。以前はどちらもしておらず、`Age` を付けず（保存済みの上流の `Age` がそのまま出ていた）、上流の `Age` も無視していたため、上流のキャッシュ（CDN など）で寿命の一部または全部を使い切ったレスポンスを、改めて `max-age` いっぱい新鮮として返していた。 |
| リクエストの `Cache-Control: no-cache`/`no-store` | — | ✅ | ルックアップをスキップ。**2026-11-03 から**、リクエストの `no-store` があれば、その応答もメモリ・ディスクともに保存しない（RFC 9111 §5.2.1.5）。Polipo は、以前の go-polipo と同じく、ルックアップを飛ばすのに使うだけで（`objectMustRevalidate`）、応答は保存していた。 |
| リクエストの `Cache-Control: max-age`/`min-fresh`/`max-stale` | — | ✅ | **実装済み（2026-11-03）。** リクエストが受け入れる保存済みレスポンスの範囲を狭めたり広げたりする（RFC 9111 §5.2.1。`isStaleFor`）。Polipo の `objectIsStale` と同じ。`max-age` は年齢の上限で、ブラウザの再読み込み（`max-age=0`）では新鮮なエントリも再検証する。`min-fresh` は残りの新鮮さがその秒数以上あることを求める。`max-stale` は期限切れをその秒数まで（値が無ければいくらでも）受け入れるが、レスポンスに `must-revalidate`・`proxy-revalidate`・`s-maxage` があれば受け入れない。`min-fresh` と `max-stale` が両方あれば、Polipo と同じく `min-fresh` を優先する。Polipo は `min-fresh` を誤って `max-age` の欄に読み込んでいた（`http_parse.c`）が、go-polipo はそうしない。以前はどれも無視しており、再読み込みしても上流に確認せずキャッシュから返していた。 |
| リクエストの `Cache-Control: only-if-cached` | — | ✅ | **実装済み（2026-11-03）。** 上流には一切接続しない。リクエストに合う保存済みレスポンス（新鮮なもの、または `max-stale` の範囲内のもの）があれば返し、無ければ `504 Object not in cache` を返す（RFC 9111 §5.2.1.7）。Polipo もキャッシュに無いときは同じ 504 を返したが、期限切れのエントリは確かめずに返していた。go-polipo はここは RFC に従う。`relaxTransparency=true` と `proxyOffline` では、ほかのリクエストと同じく期限切れのエントリを先に返す。以前はこの指定を無視し、上流に転送していた。 |
| `Pragma: no-cache`（`Cache-Control` が無いとき） | — | ✅ | **実装済み（2026-11-01）。** Polipo（`http_parse.c`）と同じく、リクエスト・レスポンスの両方で `Cache-Control: no-cache` と同じに扱う。リクエストではルックアップをスキップし（RFC 9111 §5.4 の必須要件）、レスポンスは保存するが常に期限切れとして扱う（`pragmaNoCache`。レスポンスでの意味は RFC 9111 では定められていない）。`Cache-Control` ヘッダがあれば、そちらだけで判断する。以前はどちらの方向でも無視していた。 |
| レスポンスの `no-store` / `private` | — | ✅ | ストアをスキップ。 |
| unsafe なリクエストの後の無効化 | — | ✅ | **実装済み（2026-11-03）。** unsafe なメソッド（`POST`・`PUT`・`DELETE`・`PATCH`、そのほか RFC 9110 §9.2.1 が safe としないもの）のリクエストにエラーでない応答（2xx/3xx）が返ったら、その URL のキャッシュをメモリとディスクから消す（`cacheResponse` から `CacheManager.Invalidate` を呼ぶ。RFC 9111 §4.4）。Polipo は結果を待たずにリクエストが来た時点で消しており、RFC では safe な `OPTIONS` も対象にしていた。go-polipo はどちらも RFC に従う。RFC が任意としている `Location`・`Content-Location` の URL の無効化は、Polipo と同じくしない。以前はこの処理が無く、書き込みに成功した後も古いページをキャッシュから返していた。 |
| レスポンスの `Cache-Control: no-cache` | — | ✅ | **実装済み（2026-11-01）。** 保存はするが、`lookupEntry` が常に期限切れとして扱う（`responseNoCache`）ため、`max-age` がどれだけ長くても、再利用のたびに条件付き再検証を経る（RFC 9111 §5.2.2.4。Polipo の `objectMustRevalidate` も同じく `CACHE_NO` を見る）。`no-cache="field"` の形も、フィールド指定なしと同じに扱う。2026-11-03 からは `relaxTransparency` と `proxyOffline` でも期限切れのまま返さない（それぞれの行を参照）。Polipo は返していた。以前はこの指定を無視しており、`max-age` かヒューリスティックの寿命のあいだ、上流に確認せずキャッシュから返していた。 |
| **条件付き再検証** | — | ⚠️ | **実装済み・上流方向のみ（2026-09-13 解決）。** 期限切れキャッシュエントリが `ETag` および/または `Last-Modified` を持つ場合、`ProxyHandler` の `Director`（`addConditionalHeaders`）が転送リクエストを条件付きにする。`304 Not Modified` が返れば、RFC 9111 §4.3.4 に沿って保存済みエントリへマージする（`reviveFromCache`、`ModifyResponse` 内）— `304` に含まれるヘッダはすべて保存済みのものと置き換える。ただし保存済みの本文を説明するもの（`Content-Type`・`Content-Encoding`・`Content-Length`・`Content-Range`・`Transfer-Encoding`。RFC 9111 §3.2）は除く。`304` が置き換えなかった保存済みの `Age` は消す。キャッシュ済みの本文はそのまま維持、マージ結果を再度 `Store` することで新しい鮮度期間が即座に有効になる（本文の再取得なし）。`200` が返れば従来どおりエントリを丸ごと置き換える。意図的にスコープを絞った点：go-polipo 自身が**クライアントからの**条件付きリクエストをキャッシュだけで処理する機能（RFC 9111 §4.3.5）は実装していない — クライアントが送ってきた `If-None-Match`/`If-Modified-Since` はそのまま尊重し（go-polipo 自身が合成した値で上書きしない）上流にそのまま転送するだけ。そうしたリクエストへの `304` はキャッシュへマージせず、そのままクライアントへ返す — その `304` が保証するのはクライアントの手元の版であって、保存済みの版ではないため（2026-10-02 修正 — 以前は go-polipo 自身の再検証への `304` と同じく復元しており、キャッシュが無ければクライアントに `502` を、クライアントの検証子が保存済みのものと異なればキャッシュの本文を返していた）。以前は `304` で更新するのは決まった7つのヘッダだけだった（2026-11-03 修正）。Polipo は鮮度の判断に使うヘッダ（`Date`・`Expires`・`Last-Modified`・`Cache-Control`・`Age`）だけを更新していた。 |
| **ヒューリスティック鮮度**（`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`） | (可変) | ⚠️ | **実装済み・設定不可（2026-09-11 解決）。** `Expires` / `max-age` / `s-maxage` の無い `200` は `isExpired` でヒューリスティック寿命 `min(0.1 × (Date − Last-Modified), 24h)`（`Last-Modified` 無しなら寿命なし、つまりすぐ期限切れ）を得る。`Lookup` と `-x` の両方に効く。**`maxAge` / `maxAgeFraction`：** Polipo は設定変数だったが go-polipo は同じ既定値をハードコード — 運用ポリシーではなく実質 RFC 由来の定数で、緩い値にすると今回直した過剰配信が復活するため（`cache.go` のコメント参照）。**`maxExpiresAge`**（未来の `Expires`/`max-age` をどこまで信用するかの上限）：先送り — `diskCacheUnlinkTime`（32日）が失効を頭打ちにするが、継続アクセスされるエントリには不完全。**`maxNoModifiedAge`**：条件付き再検証の実装中にベンダリングされた C 版の履歴（`object.c`）で確認したところ、これは再検証とは無関係で、`Expires`/`max-age` も `Last-Modified` も無い応答に対するヒューリスティック鮮度の上限だった（`maxNoModifiedAge = 23 * 60` 秒 = 23分）。これにより go-polipo の既存のヒューリスティック鮮度実装（`heuristicMaxLifetime`、2026-09-11 解決）に忠実性のバグがあることが判明した：go-polipo は同じ「`Last-Modified` 無し」のケースに一律24時間を使っている（Polipo の23分より約1300倍も緩い）上に、`Last-Modified` が**ある**場合の `maxAgeFraction` 計算の上限としても同じ24時間を使っているが、Polipo はそちらには `maxAge`（`14 * 24 + 1` 時間 ≈ 約14日、go-polipo の24時間よりずっと緩い）を使っている。この項目の範囲外なので今回は修正していない — ショートリスト参照。**「`Last-Modified` 無し」のケースは 2026-11-03 に解決：** ヒューリスティックの寿命を一切与えないようにした。こうした応答は保存はするが、使うたびに再検証（または再取得）する。Polipo の23分より意図的に厳しくしたもので、ブラウザや Squid の既定の `refresh_pattern` と同じ。有効期限も `Last-Modified` も無い応答は、たいていリクエストのたびに生成し直されるため。`Last-Modified` がある場合の24時間の上限はそのまま。 |
| `preciseExpiry` | `false` | 🔷 | 上記が無い以上、該当なし。 |
| `cacheIsShared` | `true` | ⚠️ | トグルは無いが、共有キャッシュのルールは部分的に強制されている。`private` は常にスキップ。認証付き応答は明示的に共有可能でない限りスキップ（ディスクキャッシュのハードニング）。 |
| `relaxTransparency` | `false` | ⚠️ | **実装済み・GET/HEAD のみ（2026-09-13 解決）。** `false`/`maybe`/`true` の三値。ベンダリングされた C 版の履歴（`http.c` の `proxyOffline`/`relaxTransparency` 直前のコメント、`client.c` での使用箇所）から確認した挙動に合わせた。`false`（既定）：上流との通信に失敗すればそのままエラー。`maybe`：上流に一切到達できないとき、`ReverseProxy` の `ErrorHandler` がエラーの代わりに stale なキャッシュを `Warning: 111 ... "Revalidation failed"` 付きで返す。上流に到達できた場合は通常どおり配信・キャッシュする。`true`：`handleForward` がネットワークに一切触れず既存エントリを配信する — 新鮮なら通常経路、stale なら `Warning: 110 ... "Object is stale"` 付き — ので、本当にキャッシュミスのときだけ上流へ到達する。go-polipo には Polipo の「再検証が必要」という独立した状態がない（期限切れエントリは無条件に丸ごと再取得する）ため、Polipo 内部の `validate`/`objectMustRevalidate` の仕組みそのものではなく、観測可能な「失敗時／stale 配信」挙動だけを移植した。CONNECT トンネルは対象外（Polipo でも `tunnel.c` にはどちらの変数も現れず、影響しない）。 **2026-11-03 から**、`maybe` でも `true` でも、`must-revalidate`・`proxy-revalidate`・`s-maxage`・`no-cache` の付いた期限切れのエントリは返さない（`staleServable`）。RFC 9111 §4.2.4・§5.2.2 が、これらは再検証に成功するまで再利用してはならないとしているため。`true` なら上流に問い合わせ、`maybe` ならエラーを返す。Polipo はこれらも期限切れのまま返していた。 |
| `proxyOffline` | `false` | ⚠️ | **実装済み・GET/HEAD のみ（2026-09-13 解決）。** true のとき `handleForward` は転送リクエストで上流に一切ダイヤルしない：キャッシュにあれば新鮮でも stale でもそのまま配信し（stale なら `Warning: 112 ... "Disconnected operation"`）、本当にキャッシュミスなら通信を試みず Polipo と同じ文言 `502 Disconnected operation and object not in cache` を返す。**2026-11-03 から**、`must-revalidate`・`proxy-revalidate`・`s-maxage`・`no-cache` の付いた期限切れのエントリは返さず、RFC 9111 §5.2.2.2 が切断中のキャッシュに求めるとおり `504 Disconnected operation and object must be revalidated` を返す。Polipo は返していた。CONNECT トンネルは対象外で、Polipo の `httpServerRequest` のゲート（`server.c`）にも手を入れていない点と一致する。 |

## 9. キャッシュ — Vary とコンテンツネゴシエーション

| 変数 / 機能 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `Vary` の扱い | — | ✅ | **Polipo に忠実な実装（2026-09-14。2026-09-11 の簡易対応を置き換え）。** キャッシュキーは今も URL のみ（`md5(url)`）で変種の *マッチング* は無い — が、実はそれこそが本家 Polipo の挙動そのものだったと判明した（当初は推測で書いていたが、ベンダリング済みの C 版履歴 `http_parse.c`/`object.c`/`client.c` で確認して訂正）。Polipo は変種ごとの保存を一切行わない。`Vary` 付き応答は単に「新鮮なヒットとして配信しない」だけで、`objectMustRevalidate` がアクセス毎にオリジンへの再検証を強制し、条件付きGET（`304`なら保存済み本文を再利用）または（有効な検証子が無ければ）フル再取得によって、実際にどの変種が正しいかをオリジン側に決めさせる。go-polipo の `lookupEntry` も同じ方式にした: `Vary` が空でない応答は普通にディスクへ書き込むが、計算上のHTTP鮮度に関わらず常に stale として報告する。*2026-09-11時点の経緯:* 当時の簡易対応（`Vary`付き応答はそもそも保存・配信しない）は、まだ条件付き再検証の仕組みが無く「安全な形でオリジンへ再検証させる」道が無かったための、より保守的な選択だった。その仕組みは2026-09-13に実装済みで、それが今回の対応を可能にした。 |
| `mindlesslyCacheVary` | `false` | ❌ | 追加せず: これは上記挙動の「オプトアウト」（再検証せずに`Vary`付きヒットを配信し、`dontTrustVaryETag`や強制再検証が本来避けようとしている「間違った変種」のリスクを受け入れる）に相当する。将来追加するのは簡単（`lookupEntry`の強制stale化をスキップするだけ）だが、今回はまさにこの項目が塞いだリスクを再び開けてしまうためスコープ外とした。 |
| `dontTrustVaryETag` | `maybe` | ✅ | **完了（2026-09-14）:** `ProxyHandler.addConditionalHeaders` は `Vary` 付きエントリの再検証で `ETag` は信用する（ただし単独の `Last-Modified` は信用しない — `client.c` の条件付きリクエストロジックが `CACHE_VARY` なオブジェクトについて `object->etag` しか見ないのと同じ）。ただし `dontTrustVaryETag` が `true` の場合はこの信用を無効化し、常にフル再取得を強制する。当初は推測で書いていたが、ベンダリング済みの C 版履歴で訂正した: Polipo の `maybe`/`false` の違い（`server.c`）は、共有されたメモリ上の `Object` を更新するか置き換えるかを判定する処理にしか関係せず、go-polipo にはその概念自体が無い（`Store` は常にファイルを丸ごと書き直すだけ）ため、この2値は go-polipo 上では同一の挙動になる。既定値は、実際のリスク（機密性の問題ではなく stale/間違った変種が配信されるだけ）と発生条件の狭さを議論した上で、より安全側の `true` ではなく Polipo 自身の既定である `maybe` のままにした。 |
| `dontCacheCookies` | (off) | ✅ | 実装済み（ディスクキャッシュのハードニング）。go-polipo では Polipo と異なり **既定 on**。 |
| `dontCacheRedirects` | (可変) | ⚠️ | 設定不可。`Store` は `200` しかキャッシュしないため `301`/`302` はそもそもキャッシュされない（Polipo より厳しいが、ノブは無い）。 |

## 10. インメモリキャッシュ、部分コンテンツ、PMM

| 機能 | 状態 | 備考 |
| :--- | :--: | :--- |
| インメモリオブジェクトキャッシュ（LRU、ハッシュテーブル、`chunkHighMark`/`objectHighMark`/…） | ✅ | **完了（2026-10-02）:** `CacheManager` はディスクキャッシュの手前に、直近使われたレスポンスのLRU（`memCache`、`github.com/hashicorp/golang-lru/v2` — go-polipo で初めてジェネリクスを実際に使った箇所）を持つ。新規変数は2つ: `memoryCacheEntries`（件数上限、既定1024）と `memoryCacheMaxEntrySize`（1件あたりの採用上限、既定1MB）。`lookupEntry` はまずメモリを見て、無ければディスクにフォールバックする。ヒットしたレスポンスはディスクヒットと**全く同じ** `isExpired`/`responseVaries` のロジックを通る（別実装のコピーではない）ため、鮮度判定やVary処理がメモリ経路とディスク経路で食い違うことはない。`Store` は本文をクライアントへ流しながら、メモリ・ディスクそれぞれが「欲しい」と判定した方にだけ写す — `maxDiskCacheEntrySize` と `memoryCacheMaxEntrySize` は独立した上限なので、片方には大きすぎても他方には収まる、ということが起こり得る。意図的にPolipoとは異なる設計にした点: `chunkHighMark`/`objectHighMark` はプロセス全体の**合計バイト数**を上限とし、個々のオブジェクトサイズに関係なくLRUで追い出す方式だが、go-polipoは**件数**で上限を切る（採用したライブラリのネイティブなモデルであり、バイト集計が要らない分シンプル）。その代わり `memoryCacheMaxEntrySize` という独立したガードを設け、少数の巨大レスポンスが多数の小さい（＝再利用されやすい）レスポンスを押し出してしまう事態を防いでいる — 真のバイト予算制キャッシュを選ばなかった理由はショートリストの項目を参照。`noDiskCache` / 空の `diskCacheRoot` とは独立して動く設計（Polipoでは `Object` のディスク表現とインメモリ表現が一体になっているのとは意図的に異なる点）— ディスクキャッシュをオフにしてもメモリキャッシュは同じように動く。再起動をまたいで永続化はされず、ディスクキャッシュというよりPolipoの「常時on」なインメモリキャッシュに近い。`-x`/`Expire`/`/polipo/flush` はこのメモリキャッシュに触れない: これらはディスク容量の回収が目的であり、メモリ常駐エントリはディスク容量を消費しないし、期限切れのエントリはLRUに何が残っていようと鮮度チェックで常にはじかれる（毎回のヒット時にその場でチェックされるため）。ディスクから返した `GET` はメモリキャッシュにも入れる（2026-11-01 追加。本文の無い HEAD は対象外）。上限は同じ `memoryCacheMaxEntrySize` なので、再起動後も、よく使うエントリは要求されるたびにメモリへ戻ってくる。LRU から追い出すときにディスクへ書き戻す処理は無い — `Store` がメモリと同時にディスクにも書くので、ディスクにはすでに同じものがある。 |
| 部分インスティレーション（持っているバイト範囲を保存し、後で隙間を埋める） | ❌ | `Store` は `200 OK` の本文全体しかキャッシュせず、`206 Partial Content` は保存しない。 |
| レンジリクエストの組み立て / キャッシュからのレンジ配信 | ⚠️ | **完全なエントリに対してのみ完了（2026-10-02）:** `ProxyHandler.serveCached` が単一レンジの `Range` リクエスト（`bytes=start-end`、`bytes=start-`、サフィックス形式 `bytes=-n`）をキャッシュヒットに対して処理し、`206 Partial Content`/`Content-Range` を返す。範囲が収まらなければ `416 Range Not Satisfiable`。`If-Range` にも対応（RFC 9110 13.1.5: エンティティの現在のETag/Last-Modifiedとの厳密比較のみを行い、一致しなければ全体を返す）。キャッシュヒット応答には常に `Accept-Ranges: bytes` を付与する。単純な ✅ にしなかった理由: マルチレンジリクエスト（`bytes=0-1,3-4`）は非対応で全体を返す方にフォールバックすること、そしてより大きな制約として、これは go-polipo が既に完全な形で持っているエンティティを切り出すだけであること（go-polipoが保存するのは常にそれだけ、下の行を参照）— キャッシュしている範囲を超えた部分をオリジンから補完して満たす、という部分キャッシュの概念は無い。クライアントにキャッシュから配信する3箇所すべて（通常のフレッシュヒット、`relaxTransparency` による古いエントリの配信、`proxyOffline`）に一律で適用。 |
| Poor Man's Multiplexing（`pmmSize`, `pmmFirstSize`） | ❌ | 大きなオブジェクトの分割取得なし。 |
| パイプライン / サーバー能力の探索（`serverSlots*`, `smallRequestTime`, `replyUnpipeline*`, `maxPipelineTrain`, `maxSideBuffering`, `serverExpireTime`） | 🔷 | コネクション再利用と多重化は `http.Transport` の責務（`MaxIdleConns:100` 固定。`IdleConnTimeout` は `serverIdleTimeout`、§3）。設定では調整不可。 |

## 11. ディスクキャッシュ

| 変数 | v1.1.x 既定 | 状態 | 備考 |
| :--- | :--- | :--: | :--- |
| `diskCacheRoot` | `/var/cache/polipo/` | ✅ | 同じ階層構造と MD5+base64 ファイル名。Polipo のキャッシュツリーを再利用可能。空値でキャッシュ無効（ディスクキャッシュのハードニング。従来は `/` に正規化されていた）。 |
| `noDiskCache` | — (go-polipo 独自) | ✅ | 新規。ルックアップと書き込みを無効化。 |
| `diskCacheUnlinkTime` | `32d` | ✅ | `-x`（`Expire`）で強制。空ディレクトリの整理つき。 |
| `maxDiskCacheEntrySize` | `-1`（無制限） | ✅ | **完了（2026-09-14）:** `Store`（`cache.go`）で強制。Polipo の `makeDiskEntry`（`diskcache.c`）と同じ判定 — 本文を読む前に宣言済みの `Content-Length` でまず判定し、チャンク転送などで長さが不明/不正確な場合は本文を流している間にフォールバック判定する（上限を超えた時点で途中までの写しを捨てる）。上限超過でもクライアントへの転送は行い、キャッシュのみスキップする。 |
| 応答を保存しながらクライアントへ流す | ✅ | **完了（2026-11-01）:** `Store` は本文を全部読み終えるまでクライアントを待たせなくなった。本文を包み、上流から届いた分をすぐクライアントへ渡すと同時に、一時ファイル（最終的な置き場所と同じディレクトリの `.tmp-*`）やメモリ上のバッファへ写す（Polipo と同じく受け取りながら返す）。本文を最後（EOF）まで読み終えたときにエントリを確定する（一時ファイルを正式な名前に rename する）。途中で閉じられた本文（クライアントの切断）や読み取りエラーで途切れた本文は捨てる。Polipo は途中までのオブジェクトも残したが、go-polipo には部分的なエントリが無い（§10）。`-x` と `/polipo/flush` は書き込み中の一時ファイルには触れず、クラッシュで残った一時ファイルは1時間更新が無ければ削除する。以前は大きなダウンロードの本文を go-polipo のメモリにまるごと貯め、届き終わるまでクライアントには何も返していなかった。 |
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
| `/polipo/` , `/polipo/config` | ✅ | 実装済み。`config` は**読み取り専用**の表。Polipo の `urlIsLocal` と同じく、ローカル形式のリクエスト（`GET /polipo/...`）にだけ応答し、プロキシ経由の `http://example.com/polipo/...` はそのサイトへ転送する（2026-11-01 修正：以前はこれにも管理画面が応答し、任意のサイトから `/polipo/config` の読み取りや `/polipo/flush` の実行ができた）。 |
| Host の検査 / ほかのサイトからの POST の拒否 | ✅ | **追加（2026-11-06）。go-polipo 独自。** Polipo は、ローカル形式のリクエストなら何にでも応答し（`urlIsLocal` は先頭の `/` しか見ない）、どこから来た POST も受け付けていた。`disableConfiguration` の既定は無効だったので、設定の変更も含む。そのため Web ページが、自前のホスト名をこのマシンに向けて（DNS リバインディング）、`/polipo/config`（パスからホームディレクトリが分かる）や `/polipo/status` を同じオリジンとして読んだり、普通のフォームで `/polipo/flush` や `/polipo/reload` に送信したりできた。go-polipo は、ローカル形式のリクエスト（`/polipo/` 以外への `404` も含む）に、Host が IP アドレス、`localhost`、マシンのホスト名、`proxyAddress` のどれかである場合にだけ応答する（`localHostAllowed`）。ページ自前のホスト名はこのどれにも当たらない。POST は、ブラウザがほかのサイトからのものと示している場合に拒否する。判定には `Sec-Fetch-Site` を使い、古いブラウザでは Host と一致しない `Origin` を使う（`http.CrossOriginProtection`）。`same-site` も拒否するので、`127.0.0.1` の別のポートのページからも送信できない。どちらの拒否も `403` で、状態表示の「Denied by policy」に数え、forbidden としてログに残す。`curl` のように、どちらのヘッダーも送らないクライアントには影響しない。 |
| `disableLocalInterface` | ✅ | 尊重。 |
| Web からの設定**編集** / `disableConfiguration` | ❌ | 未実装（ページは読み取り専用）なのでガードは無意味。 |
| `/polipo/status?`（状態表示 + キャッシュフラッシュ操作） | ✅ | **状態表示を追加（2026-11-05、`GET /polipo/status`、`stats.go`）。** Polipo のページはプロキシの動作モード（オンライン／オフライン／透過性緩和）を表示していたが、go-polipo も同様に表示する。メモリ上のオブジェクト・チャンク・アトムの数は go-polipo に相当するものが無い（§10）ため、代わりに起動以降のトラフィックの統計を表示する — 転送したリクエスト数、キャッシュヒット数とそれで返したバイト数、304 による再検証の数、オリジンへ送ったリクエスト数とオリジンのエラー数、CONNECT トンネル数と失敗数、ポリシーで拒否したリクエスト数、およびメモリキャッシュのエントリ数。統計はメモリ上にのみ保持し、再起動で 0 に戻る。ページは `Cache-Control: no-store` 付きで返す。**キャッシュフラッシュ側（2026-09-14 完了）:** Polipo自身の `/polipo/status?` アクション（`local.c`: `init-forbidden`、`reopen-log`、`discard-objects`、`writeout-objects`、`free-chunk-arenas`）は、go-polipoでは「既に別パスで対応済み」（`init-forbidden` → `POST /polipo/reload`、§4/§15）か「対応するものが無い」（残り4つはPolipoのメモリ上オブジェクトキャッシュとチャンクアリーナ向けで、go-polipoには相当する仕組みが無い — §10）のいずれかにしか当たらない。どのアクションも**ディスクキャッシュには一切触れない** — Polipoがディスクキャッシュを削除するのは別プロセスの`-x`実行時のみで、HTTP経由での操作は存在しない。つまりこの項目には移植すべき実物のアクションが元から無かった。`POST /polipo/flush`（`proxy.go`）はPolipoの移植ではなく**go-polipo独自の新設エンドポイント**で、稼働中のサーバー上で`-x`と同じ`CacheManager.Expire()`を実行し、結果件数を表示する — 特にWindowsでは`-x`を実行するための別シェルセッションが無いことが多いため有用（§15参照）。統計表示（RTT・サーバー能力など）側は下の別行の通り今回も未対応で、そもそもこの項目のスコープ外としていた。 |
| `/polipo/servers?`（サーバー別 RTT・転送レート・能力） / `disableServersList` | ❌ | サーバー統計を一切収集しない。 |
| `/polipo/index?URL`, `/polipo/recursive-index?` / `disableIndexing` | ❌ | キャッシュインデックスの閲覧なし。 |
| `localDocumentRoot`, `disableProxy` | ❌ | 単なるローカルファイルサーバーとしては動かせない。`/polipo/` 以外への直接アクセス（例：`/polipo/` を開いたブラウザが取りに来る `/favicon.ico`）や、`disableLocalInterface` 時の `/polipo/` への直接アクセスには `404 Not Found` を返す。Polipo が `localDocumentRoot` に該当ファイルが無いときに返したのと同じ（Polipo は POST には `405` を返した）。2026-11-01 修正：以前はこうした要求を転送先ホスト無しで転送しようとして、`502` と「Proxy error」のログになっていた。 |

## 14. ロギング

| 変数 | 状態 | 備考 |
| :--- | :--: | :--- |
| `logLevel` | ✅ | **完了（2026-09-14）:** 強制するようにした。`logLevel` はPolipo自身と同じ、独立したメッセージ種別のビットマスク（`log.h`の`L_ERROR`/`L_WARN`/`L_INFO`/`L_FORBIDDEN`等）であり、順序付きの重大度しきい値ではない — `logging.go`の`logf`が各既存呼び出し箇所を該当する1カテゴリで判定する、Polipo自身の`do_log`呼び出し（`if (type & LOGGING_MAX & logLevel) ...`）と同じ方式。既定値`0x07`（`LogError|LogWarn|LogInfo`）はPolipoの`LOGGING_DEFAULT`と完全一致する。挙動上の注意点: この既定値には`LogForbidden`が含まれないため、これまでの監査作業で追加してきた「Denied: ...」系のアクセス拒否ログは、ここからは既定では出力されないオプトイン扱いになる — Polipoに合わせた結果だが、この監査の少し前までのgo-polipoの挙動からは実質的な変更である。**意図的な差分（2026-11-01）:** 定義するのは go-polipo が実際に出力する4種類（`LogError`/`LogWarn`/`LogInfo`/`LogForbidden`）だけ。Polipo の `L_UNCACHEABLE`（0x10）、`L_SUPERSEDED`（0x20）、`L_VARY`（0x40）、`L_TUNNEL`（0x80、`tunnel.c` の「tunnel host:port allowed/blocked」）に当たるメッセージは go-polipo には無い。これらのビットを含む `logLevel` もエラーにはせず受け付け（Polipo の設定ファイルをそのまま読めるように）、そのビットは無視する。拒否した CONNECT はほかのポリシーによる拒否と同じく `LogForbidden` で記録し、許可した CONNECT は記録しない。以前は使われていない定数を「整合性のため」に定義しており、`config.sample` には 0x80 でトンネルのメッセージが出ると誤って書かれていた。 |
| `logFile` | ✅ | `log` の出力先を切り替え。追記モード。Polipo の `reopenLog` と同じく `SIGUSR1`/`SIGUSR2`（Unix）で開き直すので、ログローテーションができる（§15）。 |
| `logFilePermissions` | ✅ | **実装済み（2026-11-06）。** 8進数で、既定は Polipo と同じ `0640`。ログファイルを作るときに使う（umask が差し引かれる）。既にあるファイルの権限は、Polipo の `open(2)` と同じく変えない。以前は `0644` にハードコードしていたので、マシンの他のユーザーがログを読めた。ログには取得したすべての URL が残る。 |
| `logSyslog`, `logFacility` | ❌ | syslog 出力なし。 |
| `scrubLogs` | ❌ | ログからの個人情報スクラブなし。 |

## 15. デーモンとシグナル

| 機能 | 状態 | 備考 |
| :--- | :--: | :--- |
| `daemonise`, `pidFile` | 🔷 | バックグラウンド化はサービスマネージャ / Windows インストーラ + タスクスケジューラ任せ。 |
| `SIGTERM`, `SIGINT` | ✅ | `server.Close()` でグレースフル終了。 |
| `SIGHUP` | ❌ | 設定リロードなし。 |
| `SIGUSR1`（ログの開き直し + インメモリデータの書き出し + `forbidden` 再読み込み） | ✅ | **完了（2026-09-14。ログの開き直しは 2026-11-01 に追加）:** `logFile` を開き直してから、`forbidden`/`uncachable`（§4）を再読み込みする。Polipo の「書き出し」側（`writeoutObjects`）に相当する処理は無い — `Store` がメモリキャッシュと同時にディスクへも書き込むため、後から書き出すものが無い。 |
| `SIGUSR2`（ログの開き直し + メモリキャッシュ破棄 + `forbidden` 再読み込み） | ✅ | **完了（2026-09-14。メモリキャッシュの破棄とログの開き直しは 2026-11-01 に追加）:** `logFile` を開き直し、メモリキャッシュを空にしてから（`CacheManager.DiscardMemory`、§10）、`SIGUSR1` と同じ再読み込みを行う。Polipo の `discardObjects` + `initForbidden`（`event.c`）と同じ。Polipo と同じくディスクキャッシュには触れない。2026-11-01 より前は、メモリキャッシュを追加する前の名残で再読み込みだけだった。 |
| `SIGUSR1`/`SIGUSR2` でのログの開き直し | ✅ | **完了（2026-11-01）:** どちらのシグナルでも、最初に `logFile` を同じパスで開き直す（`logFileWriter.Reopen`）。Polipo の `reopenLog`（`log.c`、`event.c` から呼ばれる）と同じで、ローテーションツールがファイル名を変えてから go-polipo にシグナルを送ればよい。**意図的な差分:** 開き直せなかった場合、Polipo は終了したが、go-polipo はエラーを記録して元のファイルに書き続ける — ローテーションの失敗でプロキシを止めないため。Windows にはこれらのシグナルが無いので、ログファイルを開き直すには再起動が必要。 |

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
   固定。2026-11-03 からは 0）を得る。Polipo の既定の割合／上限に一致、ハードコード。同じ処理で
   `s-maxage` と不正 `Expires` も正しく扱うようにした。
5. ~~**宛先アドレスの検査なし（§3）。**~~ **完了（2026-11-06）:**
   `forbidLocalDestinations`（既定で有効）が、名前解決した後のアドレスで、
   このマシンやそのリンクへの接続を拒否する。DNS リバインディングで、ループバック
   だけで待ち受けるサービスに届くことはなくなった。`forbidPrivateDestinations`
   （既定で無効）はプライベートアドレスも拒否する。Polipo にはどちらも無く、意図的な追加。
6. ~~**管理画面がどの Host も、どこからの POST も受け付ける（§13）。**~~ **完了（2026-11-06）:**
   go-polipo 自身に宛てたリクエストは Host がこのマシンを指していなければならず、
   ほかのサイトからの POST は拒否する。DNS リバインディングや偽のフォームで、管理画面を
   読んだり操作したりすることはできなくなった。Polipo はどちらも検査していなかった。
7. ~~**エラー本文に Go の生のエラーが出る（§7）。**~~ **完了（2026-11-06）:**
   `502`/`504` の本文には失敗の種類だけを書き、解決後のアドレス、ポート、DNS サーバーは
   書かない。ページがこれを使って LAN を調べることはできなくなった。詳しいエラーはログに残す。

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
  判明した — §8のヒューリスティック鮮度の行を参照。（`Last-Modified` 無しの
  ケースは 2026-11-03 に 0 にした。）
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
  という挙動に対応する。`SIGUSR2` はその前にメモリキャッシュも空にする
  （2026-11-01 追加、§15 参照）。Windows には
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
- ~~Polipo 方式の `Vary` 照合（§9）。~~ **完了（2026-09-14）。この行が当初
  書いていた方針は的外れだった:** ここに書かれていた計画（リクエストごとの
  Vary 対象ヘッダ値を `X-Polipo-Vary` メタヘッダとして保存し照合する）は
  ベンダリング済みの C 版履歴を確認せずに書いた推測で、実際には Polipo に
  存在しない機能を想定していた — 本家は変種ごとの保存・照合を一切行わない。
  今回 `http_parse.c`/`object.c`/`client.c` で確認した実際の挙動: `Vary`
  付き応答も通常通りキャッシュするが、新鮮なヒットとしては絶対に配信しない
  — アクセス毎に再検証（条件付きGET、有効な検証子が無ければフル再取得）を
  強制し、オリジンの応答に「どの変種が正しいか」を決めさせる。これは当初の
  推測よりずっと小さな変更で済み、go-polipo も同じ方式にした（上記§9、
  `dontTrustVaryETag`）。URL ごとの複数変種ストアは今も対象外 — 管理が増え
  ディスク形式が非互換になる上、Polipo 自身もやっていない。
- ~~キャッシュフラッシュ操作つきの `/polipo/status?`（§13）。~~ **完了
  （2026-09-14）:** ベンダリング済みのC版履歴（`local.c`）を確認したところ、
  Polipo自身の `/polipo/status?` アクションには**ディスクキャッシュを
  フラッシュするものが一つも無い**ことが判明した — 詳細は§13参照（5つの
  実アクションのうち1つは既に別パスで対応済み、残り4つはメモリ上オブジェクト
  キャッシュが無いため対応不可）。したがって `POST /polipo/flush` は移植
  ではなく**go-polipo独自の新設エンドポイント**であり、稼働中サーバー上で
  `-x`と同じ`CacheManager.Expire`を実行し、走査/削除/解放件数を表示する
  — 特にWindowsでは`-x`を実行するための別シェルセッションが無いことが
  多く（§15参照）、再起動無しでパージできるのは実用上の利点がある。
  `disableLocalInterface`と`authCredentials`による保護は他のローカルページ
  と同様に適用し、`/polipo/reload`と同様にPOST専用にしてプリフェッチや
  クローラによる誤爆を防いでいる。
- ~~インメモリオブジェクトキャッシュ（§10）— 当初は ARCHITECTURE.md の通り
  「Go の GC / 標準ライブラリに任せれば手製アロケータ（`atom.c`/`chunk.c`/
  `object.c`）は不要」という設計上の割り切りとして C 枠に分類していたが、
  それは SSD 前提の判断であり、低速な HDD 上で運用するマシンには今でも
  効くはずだとのユーザー指摘（2026-09-14）を受けて B 枠に切り出した。~~
  **完了（2026-10-02）:** ユーザー自身の叩き台にほぼ沿って実装した —
  自前で eviction を書く代わりに `github.com/hashicorp/golang-lru/v2`
  （ジェネリクス対応。go-polipo で初めてジェネリクスを実際に使った箇所）を
  LRU として採用した点は提案(1)の通り。`CacheManager.Store` はディスクと
  メモリ両方のコピーを1回の本文読み込みから書き出すので、後から同期を
  取る必要がある状態そのものが生じない — これは提案(2)の「Storeがディスク
  を書くたびにメモリ側を更新/無効化する」という懸念を、別立てで解決する
  のではなく、設計自体でそもそも発生させない形で回避したことになる。
  ロック競合(3)は想定通り実運用上問題にならなかった — メモリキャッシュは
  ライブラリ側で自己同期しており、`CacheManager` が持つディスクI/O用の
  mutexには一切触れない。叩き台の時点では決まっていなかった論点が実装中に
  浮上した: 容量の管理方法。ユーザーと相談のうえ、件数ベース
  （`memoryCacheEntries`。`golang-lru` 自体のネイティブな容量モデルに
  合わせた — バイト予算制のモードは無い）に、1件あたりのサイズ上限
  （`memoryCacheMaxEntrySize`、既定1MB）を組み合わせる方式に決定した。
  少数の巨大レスポンスが多数の小さい（＝再利用されやすい）レスポンスを
  押し出してしまう事態を防ぐためのガードで、真のバイト予算制キャッシュ
  （`golang-lru` には無く、自前のバイト集計が必要になる）よりも精度は
  落ちるが v1 としてはシンプルな方を、ユーザーが明示的に選んだ —
  最悪メモリ使用量は（`memoryCacheMaxEntrySize` × `memoryCacheEntries`）
  で頭打ちになるが、正確な値ではない。デフォルト値（1MB × 1024件 = 最悪
  1GiB）は、当初提案した256MBからユーザー自身が引き上げたもの。副産物
  ではなく明示的に決めた点として、メモリキャッシュは `noDiskCache` /
  空の `diskCacheRoot` とは独立に動く — Polipo では `Object` のディスク
  表現とインメモリ表現が一体になっているのとは意図的に異なる。ディスク
  ヒットをメモリキャッシュへ昇格する処理は当初見送っており、起動直後の
  プロセスはそれ以降に自分で取得したものしかメモリに持たなかったが、
  2026-11-01 に実装した（§10 参照）。
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
  一連の処理が必要になる。

  ~~未着手。~~ **レンジリクエストの半分だけ完了（2026-10-02）。ユーザー
  自身の指示でこの半分に絞って着手:** この項目に最初に着手しようとした際、
  ユーザー自身が自分の指示が曖昧だったと気づき、「既に完全な形でキャッシュ
  されているエントリへのレンジアクセス」に明示的に絞り込んだ。もう一方
  の半分 — キャッシュしている範囲を超えるリクエストで、満たすには部分
  インスタンス化が無いと結局は全体再取得になってしまう — は、実運用で
  本当に必要になった時に改めて検討する方針で先送りした。このスコープで
  実際に何をカバーしたかは §10 のレンジリクエスト組み立ての行を参照
  （単一レンジのみ、`If-Range` 対応、キャッシュから配信する3箇所すべてに
  適用）。

  **部分インスタンス化そのものは先送りではなく見送りと決定（2026-10-02）。**
  設計に着手する前にユーザーから「それで実際何が得られるのか」という問いが
  あった ― きっかけとなったユースケース（同じ大きなファイルへの繰り返し・
  重複するRangeリクエスト＝動画のシーク）は個人の使い方次第だと気づいた
  ため。具体的な設計 ― 飛び飛びの範囲を管理しない「先頭からの連続範囲
  (prefix) のみ保存」、オリジン側から分かる本当のトータルサイズを示す新しい
  オンディスクマーカー、保存済みprefix内に収まるリクエストは既存の
  `writeRange` と同じ要領で配信、そして一番難しい部分 ― prefixを超える分は
  go-polipo自身がオリジンへRangeリクエストを送って取得し、保存済みの
  prefixに継ぎ足して伸長する処理 ― を検討した結果、個人的な利用に留まる
  恩恵に対して釣り合わない複雑さが見えてきた。特にロックの再設計が大きい:
  `CacheManager` の単一mutexのままだと、オリジンへの（遅いかもしれない）
  フェッチの間、他の全く無関係なキャッシュ操作まで待たされてしまうため、
  キーごとのロックへの切り替えが必要になる。ここまでは費用対効果の判断
  だけで済む話だった。実際に見送りを決定づけたのは次の点: 繰り返し
  アクセスの例として挙げられた動画自体が、今日ではほぼ例外なくHTTPSで
  配信されていること。go-polipo（Polipoも同様）はTLSを一切終端しない ―
  `proxy.go` を確認したところ、`handleConnect` はクライアントとオリジンの
  間でトンネルを張って不透明なバイト列を中継するだけで、トンネル確立前の
  `tunnelAllowedPorts`/`forbiddenTunnelsFile` によるフィルタ以外、ヘッダー
  もメソッドもキャッシュに関するロジックも一切関与しない。ディスク
  キャッシュもインメモリキャッシュも、上で実装したレンジリクエスト組み立て
  も、部分インスタンス化の有無に関わらずHTTPS通信をそもそも一切見ることが
  できない ― つまりきっかけとなったユースケースに対しては、この機能は
  最初から効果が無かったということになる。効果があり得るのは平文の
  `http://` リソース（大きなダウンロード、TLSを使わない自前ホストの
  メディアなど）だけだが、それはこの項目を立てた元々の動機ではない。
  「いつかやる」ではなく「実際のユースケースには見合わない」として
  意図的にクローズする ― 具体的に平文HTTPの大きなファイルに対する重複
  Rangeアクセスという実運用ニーズが出てきた時にのみ再検討する。§11の
  `diskCacheTruncateTime`/`diskCacheTruncateSize`（そもそもこの項目を立てた
  元々の動機）は、同じ理由で引き続き「切り詰めではなく削除」のままとする。
  PMM（1回の取得を複数の上流接続に分割する仕組み）は関連するが別の機構で
  あり、同じ理由でここでは意味を持たない。

### C. 明確に設計上の割り切り（ARCHITECTURE.md に記載済み。一文添えるだけでよい）

- PMM（§10）。
- すべての DNS ノブ（§12）、すべてのコネクションプール / パイプラインノブ（§10）。
  ただしタイムアウトは割り切りの対象ではなかった。性能の調整ではなく、止まった
  相手に引きずられないための仕組みなので、2026-11-03 に移植した（`clientTimeout`
  は §2、`serverTimeout`/`serverIdleTimeout` は §3）。
- `daemonise` / `pidFile`（§15）。
- `logSyslog` / `logFacility`（§14）— syslog への出力先が無い。

### D. 些末 / 価値が低い

- `proxyName` / `displayName`（§7）。（`disableVia` は 2026-11-03 に実装済み。）
- `forbiddenUrl` / `forbiddenRedirectCode`（§4）。
- `redirector`（§4）。
- `localDocumentRoot` / `disableProxy`、キャッシュインデックスページ（§13）。
- `diskCache*Permissions` の設定可能化（§11）。（`logFilePermissions` は 2026-11-06 に実装済み。）
- `proxyOutgoingAddress`、`useTemporarySourceAddress`（§3）。
