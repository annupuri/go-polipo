# テスト観点一覧

go-polipo のテストで確かめるべき観点と、それぞれを今どのテストが確かめているかの一覧です。テスト設計が担当者によってばらつかないようにするためのものです。観点は想像力ではなく決まった出どころから拾っているので、二人が別々にこの一覧をたどっても、同じものが見つかります。

## 使い方

- **動作を追加・変更するとき**:関係する観点を探し、そこに挙げた値の分類がどれもテストされているか確かめて、新しいテストの名前を行に書き足します。新しい設定、ヘッダー、ステータス、エラーメッセージには、行を追加します。
- **バグを直すとき**:見落としていたケースを該当する行に足します。該当する行がなければ一覧に抜けがあったということなので、行を追加します。
- **レビューするとき**:変更が関係する行をたどります。特に「抜け」の列を見ます。

各行には、試すべき**値の分類**(同値分割と、その中の境界値)、それを確かめている**テスト**(テスト関数名。`TestScenario*` は `scenario_test.go` と `scenario_parent_test.go` の手順付きシナリオ)、わかっている**抜け**を書いています。「確かめている」とは、名前が出てくるだけでなく、テストがその動作を検査しているという意味です。

## 観点の出どころ

| 出どころ | そこから拾うもの |
|---|---|
| RFC 9110 / 9111 / 9112 | プロキシとキャッシュとして go-polipo に関係する、すべての MUST と SHOULD |
| `config.sample`、`config.go` | 各設定の既定値、変更した値、不正な値 |
| `docs/PARITY_AUDIT.md` | 各行で、v1.1.x から引き継いだ動作、または選んだ違い |
| `CHANGES` | 直したバグごとに、見落としていたケース |
| エラー応答とログメッセージ | 失敗の道すじごとに、それを起こすもの |
| ISO/IEC 25010 の品質特性 | 機能以外の観点(9節) |

## 1. クライアントとアクセス制御

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| C1 | クライアントのアドレスと `allowedClients` | 既定(ループバック)、列挙した IP、CIDR、含まれない、不正な指定 | TestProxyAllowedClients, TestProxyAllowedClientsCIDR, TestProxyAllowAll | |
| C2 | `authCredentials` | 未設定、正しい、誤り、ヘッダーの形式が不正、転送/CONNECT/ローカルページそれぞれ | TestProxyAuthCredentials, TestProxyAuthCredentialsUnsetByDefault, TestScenarioRestrictedProxy | |
| C3 | オープンプロキシの警告 | ループバック以外で待ち受け、認証あり/なし | TestIsOpenProxy, TestWarnIfOpenProxy | |
| C4 | クライアントが去る | 空き待ちの間、本文の途中 | TestSemaphoreWaitEndsWhenClientLeaves, TestStoreDiscardsIncompleteBody | |
| C5 | 遅いクライアント | ヘッダーが `clientTimeout` より遅い、keep-alive で待機 | TestClientTimeout | |
| C6 | 同時実行 | `maxConcurrentRequests` 未満/ちょうど、0(無制限)、CONNECT は対象外 | TestSemaphoreWaitEndsWhenClientLeaves, semaphore_test.go | |

## 2. リクエスト

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| R1 | メソッド | GET、HEAD、POST/PUT/DELETE(安全でない)、OPTIONS、TRACE、CONNECT | TestProxyHandleForward, TestUnsafeRequestInvalidatesCache, TestMaxForwards, TestRangeIgnoredForHead | |
| R2 | リクエストターゲットの形式 | absolute、origin(ローカルページ)、authority(CONNECT)、asterisk(`OPTIONS *`) | TestProxyLocalInterface, TestProxyOtherLocalFormIsNotFound, TestProxyConnectTunnel | `OPTIONS *` |
| R3 | 接続先のポート | `allowedPorts` の内/外、`tunnelAllowedPorts` の内/外、不正 | TestProxyForwardPortNotAllowed, TestProxyHandleConnectInvalidPort, TestDefaultAllowedPorts, TestDefaultTunnelAllowedPorts, TestScenarioRestrictedProxy | |
| R4 | 接続先のアドレス | 公開、ループバック、リンクローカル、プライベート、それらが混ざって解決される名前 | TestAddressClasses, TestCheckDestination, TestForbidLocalDestinationsForward, TestForbidLocalDestinationsConnect | |
| R5 | forbidden 系の一覧 | ドメイン、サブドメイン、正規表現、ファイルを並べたディレクトリ、再読み込み | TestForbiddenMatch, TestForbiddenMatchTunnel, TestForbiddenLoadDirectory, TestProxyForbiddenFiltering, TestProxyForbiddenTunnelsFile, TestProxyReload | |
| R6 | リクエストの Cache-Control | なし、no-cache、no-store、max-age(0/n/不正)、max-stale(なし/値なし/n/不正)、min-fresh、only-if-cached、複数行 | TestRequestCacheControlBoundsFreshness, TestLookupReadsEveryRequestCacheControlLine, TestStoreSkipsRequestNoStore, TestOnlyIfCached, TestReloadRevalidatesFreshEntry | |
| R7 | Pragma | Cache-Control の有無と `no-cache` | TestPragmaNoCache | |
| R8 | クライアント自身の条件付きヘッダー | If-None-Match、If-Modified-Since、キャッシュの有無 | TestClientConditionalHeaderNotOverridden, TestClient304PassesThroughWithoutCachedEntry | |
| R9 | Range | 単一、終端なし、末尾指定、複数、満たせない、不正、If-Range の一致/不一致 | TestParseByteRange, TestRangeServedFromCache, TestRangeUnsatisfiableFromCache, TestRangeIgnoredWhenNotApplicable, TestRangeIfRangeMatchServesPartial, TestRangeIfRangeMismatchServesWhole | |
| R10 | ホップバイホップと転送系のヘッダー | エンドツーエンドのヘッダーを指す Connection、X-Forwarded-*、Forwarded、`censoredHeaders`、Referer(`censorReferer`) | TestForwardedRequestHeaders, TestConditionalHeadersSurviveConnection, TestCensoredHeadersStrippedFromRequest, TestCensorReferer* | |
| R11 | Via、Max-Forwards | Via のループ、`disableVia`、Max-Forwards 0/n/不正 | TestVia, TestViaLoopDetected, TestMaxForwards | |
| R12 | 認証付きリクエスト | Authorization あり | TestStoreSkipsAuthenticated | |
| R13 | Expect: 100-continue | 本文付きリクエスト | | 未テスト |

## 3. オリジンの応答

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| O1 | ステータス | 200、206、304、3xx、4xx、5xx、407(親プロキシの有無) | TestCacheInvalidAndNonGetCases, TestConditionalRevalidation304ServesCachedBodyAndRefreshes, TestParentProxyRefusesCredentials | |
| O2 | 応答の Cache-Control | max-age、s-maxage、no-store、no-cache、private、must-revalidate、proxy-revalidate、複数行 | TestFreshnessLifetime, TestStoreReadsEveryCacheControlLine, TestStoreCachesNoCacheButAlwaysStale, TestStaleForbiddenEntryNotServedStale | |
| O3 | Cache-Control がないときの鮮度 | Expires、Last-Modified からの推定、どちらもない、Date がない | TestHeuristicLifetime, TestFreshnessLifetime, TestMissingDateIsAppended | |
| O4 | 検証子 | 強い/弱い ETag、Last-Modified、なし | TestConditionalRevalidationSendsIfModifiedSince, TestNoConditionalHeadersWithoutValidators | |
| O5 | Vary | なし、ETag 付きの Vary、ETag なしの Vary、`dontTrustVaryETag` | TestStoreCachesVaryButAlwaysStale, TestAddConditionalHeadersVary*, TestAddConditionalHeadersDontTrustVaryETagTrue | |
| O6 | Set-Cookie | あり、`dontCacheCookies` | TestStoreSkipsSetCookie | |
| O7 | 本文の長さ | Content-Length、chunked、切断で終わる、`maxDiskCacheEntrySize`/`memoryCacheMaxEntrySize` 超え、途中で上限を超える | TestStoreSkipsOversizedEntries, TestStoreDropsBodyThatGrowsPastLimit, TestMemoryCacheMaxEntrySize | |
| O8 | Content-Encoding | オリジンからの gzip、勝手に圧縮しない | TestNoTransparentCompression | |

## 4. オリジンとネットワークの状態

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| N1 | オリジンに届くか | 正常、拒否、ホスト名がない、タイムアウト(`serverTimeout`)、本文の途中で止まる、本文の途中で切れる | TestProxyForwardRoundTripError, TestUpstreamErrorReply, TestServerTimeoutAwaitingHeaders, TestServerTimeoutMidBody, TestStoreDiscardsIncompleteBody | |
| N2 | 接続の再利用 | 再利用、`serverIdleTimeout` | TestServerTimeoutOnReusedConnection | `serverIdleTimeout` そのもの |
| N3 | エラー応答の本文 | ネットワークの情報を漏らさない | TestUpstreamErrorBodyRevealsNothing | |

## 5. キャッシュの状態

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| S1 | エントリ | なし、メモリの新鮮なヒット、ディスクの新鮮なヒット、古い、古いが古いまま返せない、Vary/no-cache のエントリ | TestCacheStoreAndLookup, TestDiskHitPromotedToMemory, TestMemoryCacheRespectsFreshnessAndVary, TestVaryEntryNeverServedWithoutRevalidation, TestScenarioCacheLifecycle | |
| S2 | 再検証 | 304 でヘッダーを合成、200 で置き換え、エントリなしの 304 | TestConditionalRevalidation200ReplacesCache, TestRevalidation304UpdatesStoredHeaders, TestRevalidation304WithoutCachedEntryIsAnError | |
| S3 | 階層 | ディスクなし、メモリなし、両方なし、追い出し | TestDiskCacheDisabled, TestMemoryCacheDisabled, TestMemoryCacheEntriesEviction, TestMemoryCacheIndependentOfNoDiskCache | |
| S4 | ディスク上のファイル | 壊れている、一時ファイル(書き込み中/放置)、`diskCacheUnlinkTime` を過ぎて未使用、大きくて未使用(`diskCacheTruncate*`) | TestExpire, TestExpireTemporaryFiles, TestExpireRemovesLargeStaleEntries | |
| S5 | ディスクのエラー | キャッシュディレクトリに書けない、ディスクがいっぱい | | 未テスト |
| S6 | 無効化 | 安全でないメソッドがその URL を無効にする | TestUnsafeRequestInvalidatesCache | |

## 6. デシジョンテーブル

まとめて確かめる価値のある組み合わせです。各マスが1つのテストケースです。

**オリジンに届かないとき(`relaxTransparency` × エントリ)**

| エントリ | false | maybe | true |
|---|---|---|---|
| 新鮮 | 返す(接続しない) | 返す | 返す |
| 古い、古いまま返せる | 502 | 古いもの + Warning 111 | 古いもの + Warning 110、接続しない |
| 古い、must-revalidate | 502 | 502 | 502 |
| なし | 502 | 502 | 502 |

テスト:TestRelaxTransparency*, TestStaleForbiddenEntryNotServedStale, TestScenarioCacheLifecycle。

**`proxyOffline`(× エントリ)**:新鮮 → 返す、古いまま返せる → Warning 112、must-revalidate → 504、なし → 502。テスト:TestProxyOffline*, TestScenarioOffline。

**保存するか(メソッド × ステータス × リクエスト/応答の指示 × 認証 × Cookie × サイズ)**:TestCacheInvalidAndNonGetCases, TestStoreSkips*。

## 7. 上流への経路

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| U1 | 直接 | HTTP、CONNECT | TestProxyHandleForward, TestProxyConnectTunnel, TestScenarioHTTPS | |
| U2 | HTTP の親プロキシ | 転送、CONNECT、認証情報が正しい/誤り/URL に記載、親が拒否 | TestParentProxyAuthCredentials, TestParentProxyRefusesCredentials, TestConnectThroughParentProxy*, TestScenarioHTTPParentProxy | `https://` の親 |
| U3 | SOCKS5 の親プロキシ | 転送、CONNECT、認証情報が正しい/誤り | TestDialContextSocks, TestSocksAuthCredentials, TestScenarioSocksParentProxy | |
| U4 | SOCKS 経由の HTTP 親プロキシ | 両方を設定 | | 未テスト |
| U5 | トンネルの寿命 | 通信中、`tunnelIdleTimeout` を超えて無通信、片側だけ閉じる | TestTunnelIdleTimeout*, TestTunnelOutlivesTimeouts, TestConnectForwardsBytesSentWithTheRequest | `tunnelIdleTimeout` 設定時の片側クローズ |

## 8. 運用

| # | 観点 | 値の分類 | テスト | 抜け |
|---|---|---|---|---|
| P1 | 起動 | 設定ファイルなし、正しい、不正な行、知らない変数、コマンドラインの `var=val` | TestRunBadConfigFile, TestRunBadCommandLineVar, TestLoadConfig, TestParseLine* | |
| P2 | 値の書き方 | 型ごと:bool、int(16進/8進)、時間、tristate、ポートの集合、リスト、引用符付き | TestSetAllKeys, TestParseLevelFormats, TestParseDuration, TestParsePortSet, TestRelaxTransparencyTristate | |
| P3 | コマンド | `-h`、`-v`、`-x` | TestRunHelp, TestRunVersion, TestRunExpire, TestRunExpireMissingRoot | |
| P4 | ローカルページ | トップ、config、status、flush、reload、メソッド、よその Host、クロスサイトの POST、`disableLocalInterface` | TestProxyLocalInterface, TestStatusPage, TestProxyFlushEndpoint, TestProxyReloadEndpoint, TestLocalInterfaceRefuses*, TestScenarioLocalPages | |
| P5 | シグナル | SIGUSR1/SIGUSR2、SIGINT/SIGTERM | TestSetupReloadSignals | SIGTERM での終了 |
| P6 | ログ | レベル、`logFile`、パーミッション、開き直し | TestLogfGatesByCategory, TestLogFileWriterReopen*, TestLogFilePermissions, TestRunLogFileWritten | |
| P7 | パスワードの表示 | `-v`、`/polipo/config`、ログ | TestPasswordEntriesAreRedacted, TestParentProxyPasswordHidden, TestInvalidParentProxyWarningHidesPassword | |

## 9. 機能以外(ISO/IEC 25010)

| 品質特性 | go-polipo での観点 | テスト |
|---|---|---|
| セキュリティ | オープンプロキシ、SSRF/DNS リバインディング(R4)、XST(TRACE)、ローカルページへのクロスサイト POST、エラー本文、パスワード(P7) | 挙げた行を参照 |
| 信頼性 | オリジンの障害(N1)、オフライン(6節)、ゴルーチン内の panic | |
| 性能効率 | 本文全体をためずに流す、同時実行の上限(C6) | TestStoreStreamsBodyWhileCaching |
| 互換性 | Polipo のディスクキャッシュ形式、設定の書き方 | TestCachePathGeneration, TestLoadConfig |
| 移植性 | Linux、Windows(SIGUSR がない、`polipo.exe`) | CI は Linux だけ。Windows は手元でビルド(`make build-windows`)するだけで、テストしていない |

## わかっている抜け

上の表から集めたものです。次にテストに取り組む人のために並べておきます:R2 `OPTIONS *`、R13 Expect: 100-continue、N2 `serverIdleTimeout`、S5 ディスクのエラー、U2 `https://` の親、U4 SOCKS 経由の HTTP 親プロキシ、U5 `tunnelIdleTimeout` 設定時の片側クローズ、P5 SIGTERM での終了、Windows の CI。
