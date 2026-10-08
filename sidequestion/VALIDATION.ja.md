# `/btw` 検証記録

日付：2026-09-10。ブランチ：`codex/btw-side-question`。ベースライン：`8dae851b9b622f2ff2631f332fde9719d0b16fba`。

独立した PostgreSQL テストデータベースとデータディレクトリを使用。実モデルの認証情報は独立したテスト環境にのみ注入し、コードや本記録には書き込んでおらず、製品のデフォルトモデルも変更していません。Go 1.26.3、norma v0.3.6、Next.js 16.2.9。

実際のモデル対話、返却オブジェクト、エンジニアリングのアサーション、および Qwen の審査原文は [validation-2026-09-10.json](validation-2026-09-10.json) に保存されており、その中に API 認証情報は含まれていません。

## エンジニアリングチェック

| 範囲 | 結果 | 証拠 |
| --- | --- | --- |
| 構造化メッセージ、ツール引数のディープコピー | 合格 | `TestCheckpointDeepCopyAndBoundaries` |
| 要約／圧縮リクエストが上書きしないこと、完全な応答と終了状態の公開、部分的な応答の除外 | 合格 | `TestCheckpointDeepCopyAndBoundaries`、`TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 実際のモデルプールメンバーの ID | 合格 | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| ツールペアリング、20 組の再生、予算の裁断と超過エラー | 合格 | `TestBuildRequestCompactionToolPairingAndBudget` |
| メインとサイドバーの並行実行、双方向キャンセルの隔離 | 合格 | ブロッキング式 Provider、`TestMainSideConcurrencyAndIndependentCancellation` |
| ツール実行なし、ストリーミング／非ストリーミング、失敗時の既取得使用量 | 合格 | `TestServiceNoToolsAndUsageOnFailure` |
| 実際の norma ChatAgent + ローカル Read ツール、メイン transcript／アクティビティの隔離 | 合格 | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`、ストリーミングと非ストリーミングのサブケース |
| 永続化、ページング、冪等性、再起動後の部分回答保持 | 合格 | `TestSideHistoryIdempotencyPagingAndRecovery` |
| クリアと遅延書き込みの競合、親リソースの削除、バージョン比較 | 合格 | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent／Worker のアーカイブと復元、v1/v2/v3 | 合格 | `TestSideTaskArchiveVersions` |
| 3 種類の親インターフェース、認証、リソース帰属、Worker の論理削除 | 合格 | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`、`TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| ビジーなメインセッションでもサイドバー可能、独立 SSE の再接続／切断、キャンセル、クリア | 合格 | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 親セッションごと 1／グローバル 4 の並行数 | 合格 | 2 つの `TestSideHTTP…` ケース |
| 送信前のスナップショット保存、再起動後の継続質問、古いセッションでスナップショットを偽造できないこと | 合格 | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| キャッシュ内の設定が削除またはモデルが変更された後に継続を拒否 | 合格 | `TestSideRejectsDeletedOrChangedCachedProfile` |
| アーカイブ前にキャンセルし、最終回答と使用量の保存を待機 | 合格 | `TestSideTaskDrainPersistsBeforeArchive` |
| ストリーミング消費者が早期にキャンセルした場合、使用量を 1 回のみ記録しサイドバーに帰属 | 合格 | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| 再起動後に自動復元された Worker／deadline 実行コンテキストが新しいスナップショットを公開し続ける | 合格 | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| 関連パッケージの race チェック | 合格 | 下記コマンド |
| TypeScript と本番ビルド | 合格 | `npx tsc --noEmit`、`npm run build` |
| 新規フロントエンドモジュールの Biome | 合格 | `biome check`、新規 3 モジュール |

破棄可能な別のデータベースで `ARTEX_PG_DSN` を設定すれば、自動化チェックを再現できます（本番データベースを指定しないこと）：

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

Go の全量リグレッションは完全にグリーンではありません。`server` パッケージには、一時ディレクトリのクリーンアップ段階で失敗する既存テストが 2 つあり、いずれも `TempDir RemoveAll … directory not empty` を報告します：

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

上記の未変更ベースラインからソースコードをエクスポートし、同じ隔離環境で `server` パッケージを再実行したところ、この 2 つのクリーンアップ失敗も再現しました。ベースラインの実行ではさらに `TestCoreTaskLifecyclePG` のターゲットノード数のアサーション失敗が発生しましたが、最終的に変更後の `server` リグレッションではそのアサーション失敗はありません。その他のパッケージは合格し、今回のサイドバー関連ケースおよび race チェックも合格しています。ベースラインの問題を今回の受け入れ合格として扱っておらず、問題を隠すために既存のアサーションを変更してもいません。

Next.js のビルド出力には既存の複数 lockfile／workspace root 推論の警告が出ますが、ビルドは完了し、すべてのページが正常に生成されました。

## ブラウザチェック

Codex In-app Browser を使用し、独立したローカル Go サービスと Next.js 開発サーバーに接続。デスクトップと 390 × 844 の狭い画面で以下の手動自動操作を実行し、スクリーンショットとブラウザログを確認しました：

- 通常のチャット実行中に `/btw` を入力し、メインコンテンツとサイドバーを同時に表示。デスクトップのサイドバーは正常。
- 連続して追問。サイドバー停止後も生成済みの部分を保持し、メイン処理は継続。
- パネルを閉じてもリクエストは継続し、再度開くと完了した回答を復元。ページ更新後、空の `/btw` で履歴を復元。
- 狭い画面の Drawer の入力、ボタン、履歴、閉じる操作が正常で、横方向オーバーフローなし。
- クリアは確認ダイアログを使用し、クリア後は履歴が消え、メイン transcript とスナップショットは保持。
- タスクの MainAgent と 2 つの Worker でそれぞれ質問して切り替え、Agent のラベルと履歴が混線しないこと。
- ブロッキング式のローカルモデルフィクスチャで Worker を実行状態に保ち、Worker のメイン入力欄から `/btw` を送信。サイドバー停止後も Worker はリアルタイム実行と自身の一時停止ボタンを表示し続け、サイドバーは部分回答を保存。
- ブラウザのエラー／警告ログは空。

制御可能なフィクスチャは並行時のタイミングを正確に検証するために使用し、実モデルの出力速度に依存しません。デバッグ中、2 回の Worker 実行時のチェックで有効な並行ウィンドウが形成されませんでした（タスクがすでに終了／回答が早期に終了）。フィクスチャを修正してやり直し、合格しました。これらの初期操作は有効な合格として記録しません。

## 実モデル対話

優先的に `grok-4.6` を探査。OpenAI 互換インターフェース `http://127.0.0.1:12580/tingly/openai`。探査は HTTP 200 で、モデル名 `grok-4.6` と `READY` を返し、所要 2.82 秒。第一候補が利用可能だったため、Tingly `glm` や智谱 `glm-5.3` のバックアップチェーンは有効化しておらず、これら 2 つのバックアップサービスは今回検証していません。

| シナリオ | 実際の結果 |
| --- | --- |
| メインセッション実行中に資産、目標、マーカーを質問 | `redhaze.top`、トップページの読み取りと目標の要約、`BTW-REAL-0910` を返却。サイドバー完了、16.97 秒 |
| メインセッションがトップページ読み取りを完了した後にツールの根拠を質問 | WebFetch 200、curl リダイレクト 301 → 302 → 200、ページタイトルを正しく引用。7.24 秒 |
| サイドバーが Bash でテストファイルの作成を要求 | 実行を拒否、対象ファイルは作成されず。7.74 秒 |
| 完了後のサイドバーがメインコンテキストを変更しないこと | メイン transcript の SHA-256 とメインアクティビティ記録が一致。サイドバーのツール実行回数は 0 |
| Go サービスを本当に停止／再起動した後の継続質問 | 以前の 3 件のサイドバー履歴を保持し、永続化されたスナップショットから直接、資産、マーカー、タイトルに回答。メイン Agent は再実行せず |
| 新しいセッションで Grok の非ストリーミング設定を使用 | 資産と `ATOMIC-0910` に正しく回答。使用量を返却して保存：input 11734、output 138、cache_read 11520 |

資産ケースのメインセッションは WebFetch と Bash/curl で公開トップページを読み取り、ランディングページは `https://id.redhaze.top/home`、タイトルは「红幕科技 RedHaze Group · 全球综合集团门户」でした。Bash は応答をローカルのテストファイルに一時保存しており、リモートへの書き込みは実行していません。この事実は「サイドバーがツールを実行していない」ことと分けて検証しました。

メイン transcript の検証値：`e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`。

**使用量に関する制約：** Tingly の Grok のストリーミング応答は usage を返しませんでした。別途 `stream_options.include_usage=true` を直接送信して検証したところ、HTTP 200、データフレーム 12 個、usage フレーム 0 個でした。したがってストリーミングテストでの 0 はエンドポイントが使用量を提供していないことを示すものであり、課金がないと解釈することはできません。非ストリーミングの使用量、およびフィクスチャの失敗／キャンセル時の使用量はいずれも正しく保存されました。

## Qwen 審査

審査モデル `qwen-flash`、OpenAI 互換インターフェース `https://dashscope.aliyuncs.com/compatible-mode/v1`、HTTP 200。最初の 3 件の実サイドバー対話、メインセッションのツール根拠、エンジニアリングのアサーションを提供したところ、`verdict: accept`、`concerns: []` を返し、回答は資産、マーカー、ページ読み取りの証拠と一致し、サイドバーのツール拒否は制約に適合していると判断しました。審査の使用量：prompt 6625、completion 312、total 6937。

この Qwen 審査の範囲には、後から追加したサービス再起動と非ストリーミングテストは含まれていません。Qwen の「書き込みなし」という概括はやや広すぎます。メインセッションの curl は確かにローカルの応答一時ファイルを作成しており、これは上記で明確に記録済みです。並行性、ゼロツール実行、transcript の隔離はエンジニアリングのアサーションによって判定しており、モデル審査は回答品質の評価を補助するものにすぎません。
