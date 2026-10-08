---
name: api-recon
description: ウェブサイトのAPIインターフェースを収集する際にこのskillを呼び出す。
---

# API Recon（フロントエンドインターフェース偵察）

**認可済み**であることを前提に、以下をできる限り網羅的に発見する：**バックエンド API**（パス、メソッド、パラメータ、レスポンスボディ）、**フロントエンドルート**、**UI機能トリガーポイント**（Tab、ダイアログ、テーブル操作など）。

---

## 境界と禁止事項（Agent 必読 · 違反は越境とみなす）

本 skill は **API / パラメータ面の偵察のみ**を行い、脆弱性の発掘や侵入・悪用のフェーズではない。

### タスクの境界

| 範囲 | 許可 | 禁止 |
|---|---|---|
| **目標** | path、method、パラメータ、ルート、UIトリガーポイントの列挙 | SQLi/XSS/権限昇格/総当たり/fuzz 脆弱性、パケット改ざん攻撃、破壊的操作 |
| **認証** | Hook + stub/mock による**クライアント側**ログインゲートの回避 | ユーザーへのアカウント・パスワードの要求や推測、実際のログインフォーム送信の試行 |
| **ランタイム** | 資格情報なしでインターフェースを hook し、mock レスポンスで SPA をログイン後のシェル層に入らせる | 継続に実際のバックエンドセッションを必要とするフロー |

### 資格情報なしの動的解析（Phase 3 デフォルト）

1. `preload.js` / `runtime_harvest.js` によってログイン・権限・メニューなどの bootstrap インターフェースを**傍受して stub** する。
2. 業務クエリインターフェースに対して **構造が正しく、業務コードが成功で、データは空でもよい** mock body を返す。
3. フロントエンドがバックエンドなし、または 401 の環境下でもログイン後のページをレンダリングできるようにし、より多くの XHR/fetch/WebSocket をトリガーする。
4. **空データ、空のテーブル、プレースホルダ UI はいずれも想定内**——これを理由に実際のログインや脆弱性テストへ転じないこと。

**一言で言えば**：mock でフロントエンドのルートとコンポーネントのマウントを押し広げ、**outbound リクエストのみを記録する**。バックエンドが何を返すかは重要ではなく、重要なのはフロントエンドが**他にどんなインターフェースを送るか**である。

### フロー上の厳格な禁止事項

| 禁止 | 代替手法 |
|---|---|
| Phase 1 完了前に grep/curl/Read でメイン entry `index-*.js` から API path を抽出する | `OUTDIR/harvest_static.py` を実行する |
| harvest を代替する `extract_apis.py` などのスクリプトを手書きする | `OUTDIR/harvest_static.py` を修正して再実行する |
| 同一の grep/コマンドが2回以上失敗しても繰り返す | 戦略を変える：tool_logs を読む、harvest を修正する、reference を調べる |
| ゲート A/B を飛ばして `scripts/` のオリジナル版を直接実行する | OUTDIR にコピーして目標に合わせて修正する |
| 実際のユーザー名/パスワード、OTP、OAuth などの認証 | stub/mock（上記参照） |
| 「実データを取得する」ことを理由に stub を飛ばし、権限昇格/インジェクションテストを行う | outbound のみ記録し、recon の境界に留める |
| 削除、機密データのエクスポート、一括書き込みなどの不可逆操作 | coverage のクリックも同様 |
| runtime + 動的列挙を完了せずに、全ページと全インターフェースを取得したと主張する | 「完了の定義」を参照するか、限界を明記する |
| パラメータトリガー行列 + diff を完了せずに、全パラメータを把握したと主張する | Phase 3b 行列 + Phase 5 diff |
| 単一の runtime サンプルから必須/任意を推論する | 複数サンプルの diff、または検証ルール/エラーからの逆推定 |

---

## 二層モデル + 実行モード

| 層 | 産出物 | 上限 |
|---|---|---|
| **静的**（JS bundle） | 全 endpoint パス、ルート草案、組み立てポイントのフィールド候補 | HTTP メソッドなし、パラメータは Phase 1b が必要、ランタイムで連結される URL を取りこぼす |
| **ランタイム**（アクティブセッション） | メソッド + body + レスポンス + 動的 URL + WS/SSE、複数サンプルの diff でパラメータを補完 | ページが実際にレンダリングされないとリクエストが送られない、単一サンプルでは必須/任意を確定できない |

| 実行モード | エンジン | 適用 |
|---|---|---|
| **depth** | `runtime_harvest.js`（Puppeteer） | API 一覧、METHOD/params/レスポンスボディ、WS/SSE、再現可能な一括実行 |
| **coverage** | browser + `preload.js` | Tab/ダイアログ/テーブルのクリック、機能ポイントのカバレッジがより深い |
| **both** | まず depth、次に coverage | 最も網羅的、最も時間がかかる |

**パラメータ方法論**（汎用スクリプトなし）：path は harvest/正規表現を使う、パラメータは **アンカー窓拡張 + UIバインディングチェーン + 複数サンプル diff + エラー逆推定** を使う（grep レシピは [reference.md](reference.md) J節を参照）。

---

## 完了の定義

すべてを満たして初めて recon 完了と主張できる：

- [ ] **静的**：Phase 1 harvest が `api_static.txt`、`routes.txt`、`js/` を産出
- [ ] **ランタイム**：depth または coverage の少なくとも一方、coverage/both は **Hook 有効 + 動的列挙ループ** が必要
- [ ] **シェル進入**：業務 path へアクセスした際に `/login` でない（hash ルートに注意）
- [ ] **パラメータ**：coverage/both はパラメータトリガー行列 + `param_samples.json` を完了、Phase 5 で `params_merged.json` に統合
- [ ] **深掘り**（モジュールページが空白の場合）：Phase 4 で権限ツリーを復元して再実行し、**モジュールレベルの API**（locale/bootstrap のみでない）が現れるまで行う
- [ ] **成果物**：Phase 5 の産出物が揃っている（Phase 5 産出物表を参照）、`insert_assets` でサービスおよびエンドポイント資産を書き込む

---

## スクリプトとゲート

`scripts/` はあくまで参考テンプレートであり、オリジナル版を直接実行して最終結果とすることを**禁止する**。

**ルール**：まず読む → 目標に合わせて修正 → `OUTDIR`（例：`recon/`）に書き込む → `CHANGES.md` に記録。合致しない場合は方法論に従って書き直し、構造だけを借りる。

| ゲート | タイミング | 参考スクリプト → OUTDIR コピー | よくある必須修正項目 |
|---|---|---|---|
| **A（静的）** | Phase 0 後、**初回**の harvest/spider 実行前 | `harvest_static.py` / `spider_mpa.py` | **多くのサイトではデフォルトの regex でそのまま実行可能**、manifest/方言が合致しない場合のみ endpoint 正規表現、webpack/Vite `publicPath`、MPA exclude/cookie を修正 |
| **B（ランタイム）** | Phase 2 後、depth/coverage 実行前 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage のキー、neutralize 成功値、stubs、login 正規表現、api プレフィックス、hash/history |

**SPA 強制順序**（交換不可、Phase 番号は「まず探索してからスクリプト」に優先する）：

| ステップ | 必須 | 禁止 |
|---|---|---|
| Phase 0 完了後 | 次の Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read でメイン entry `index-*.js`（通常 >500KB） |
| ゲート A | スクリプトをコピー → 必要に応じて小修正 → **即座に実行** | 先に手動で API を抽出してから harvest するか決める |
| Phase 1 完了前 | `wc -l` で産出物を検証、404 は harvest を修正して再試行 | extract スクリプトの手書き、未ダウンロード URL への繰り返し grep |
| Phase 1b から | grep は `OUTDIR/js/*.js` のみ | メイン bundle を harvest の代わりに使う |

- ✅ `harvest_static.py` をコピー → （任意で）regex を修正 → **即座に実行**
- ❌ メイン bundle を curl → 何度も grep → 一時的な extract を書く → 最後にようやく harvest
- **MPA**：Phase 0 後の次の Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## ツールと出力の制約

| 制約 | 説明 |
|---|---|
| 大きいファイル | >100KB の `index-*.js` を Read/grep でコンテキストに入れることを**禁止**、OUTDIR スクリプトでバッチ処理する |
| grep 出力 | 必ず `\| head -20` または `-m 5`、対話には path のサマリのみ残し、bundle の断片を貼らない |
| 検証 | `wc -l`、`ls \| wc -l` を使う、ディレクトリ全体を Read しない |
| regex 初探 | 任意、1回以下、≤50KB の小さい chunk または HTML のみ、正式な静的解析は harvest を基準とする |
| reference | レシピ/テンプレート/トラブルシュートは [reference.md](reference.md) を参照、全文を inline で重複させない |

---

## 実行ロードマップ

```
Phase 0 分類 + OUTDIR
  → ゲート A → Phase 1 harvest（★ 即座に実行 ★）
  → Phase 1b パラメータ逆解析
  → Phase 2 認証三つのゲート → config.json
  → ゲート B → Phase 3 ランタイム + パラメータ行列
  → Phase 4 権限ツリー（必要時）→ Phase 3 を再実行
  → Phase 5 統合レポート + insert_assets で発見した全サービス・エンドポイントapi資産を一括挿入、いかなる場合も挿入時に発見済み資産を取りこぼすことは許されない
```

順に沿ってチェックする。**前の項目が未完了の間は次の Phase に進んではならない。**

1. [ ] **Phase 0**：SPA/MPA を初探、`OUTDIR` を作成 → [Phase 0](#phase-0--分类)
2. [ ] **ゲート A + Phase 1**：スクリプトをコピー → **即座に** harvest → `wc -l` で検証 → [Phase 1](#phase-1--静态)
3. [ ] **Phase 1b**：アンカー窓拡張 + バインディング層 → `param_candidates.json` → [Phase 1b](#phase-1b--参数逆向)
4. [ ] **Phase 2**：認証三つのゲート → `config.json` → [Phase 2](#phase-2--鉴权三道门)
5. [ ] **ゲート B**：runtime スクリプトを調整 → [Phase 3](#phase-3--运行时)
6. [ ] **Phase 3**：depth / coverage / both、シェル進入を確認、パラメータトリガー行列 → `param_samples.json`
7. [ ] **Phase 4**（必要な場合）：権限ツリー → patch stubs → Phase 3 を再実行 → [Phase 4](#phase-4--权限树还原)
8. [ ] **Phase 5**：産出物を統合 + レポート + `insert_assets` → [Phase 5](#phase-5--合并与报告)

---

## Phase 0 — 分類

エントリー HTML を取得し、**`OUTDIR` を作成する**（skill 内の `scripts/` を変更しない）：

- **SPA**：空シェル + `<div id=app>` + chunk → Phase 1–5
- **MPA**：SSR + `<form>`、endpoint bundle なし → ゲート A 後：

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

`forms.txt`、`links.txt`、`api_inline.txt` を産出する。SPA で forms ≈ 0 の場合 → Phase 1 に切り替える。

---

## Phase 1 — 静的

[スクリプトとゲート](#脚本与门禁) · [ツールと出力の制約](#工具与输出约束) に従う。

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest：HTML script を解析 → webpack/Vite manifest → すべての lazy chunk をダウンロード → `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt` を産出。

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 数 vs manifest：404 は harvest を修正して再試行する、chunk を1つずつ手動で curl しない
- `api_static.txt` が少なすぎる → OUTDIR 内の endpoint 正規表現を緩めてから再実行（reference 参照）

### Phase 1b — パラメータ逆解析

path は Phase 1 から得る、パラメータフィールドは別途 recon が必要。grep ルールは [ツールと出力の制約](#工具与输出约束) を参照。

**完了基準**：重要なインターフェースについて——フィールド名、伝送位置、型推論、必須かどうか、サンプル値、信頼度——に答えられること。

#### 1b.0 — 伝送形態

| 形態 | パラメータの所在 | 静的解析で優先的に見る箇所 |
|---|---|---|
| REST JSON | body + query | path アンカー付近の `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql テンプレート、`$page: Int` |
| 従来の form | urlencoded | `<form>`、`FormData` |
| ファイルアップロード | multipart | `FormData.append` |
| パスパラメータ | `/user/:id` | ルートテーブル + `useParams` / `$route.params` |
| 暗号化/署名 | `sign`/`data` に包まれる | 暗号化関数の引数を Hook（reference D節） |

産出物：各インターフェースに `transport: query|json|form|graphql|encrypted` を付記する。

#### 1b.1 — アンカー窓拡張

既知の path をアンカーとし、窓を拡張して組み立てオブジェクトを探す：

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| ラッパー層 | パラメータの手がかり |
|---|---|
| axios インスタンス | `data` / `params` |
| 統一 request | インターセプタがグローバルフィールドを注入 |
| OpenAPI クライアント | 生成された method シグネチャ |
| React Query / SWR | hook の第2引数 |
| Vue composable | composable の引数 |

型の残留：`yup`/`zod`/rules、`Form.Item name=`、埋め込みの Swagger。

→ `param_candidates.json`：`{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — バインディング層

```
Form field → onFinish/handleSubmit → transform → API payload
```

| バインディング元 | 手法 |
|---|---|
| フォーム submit | submit → transform → API を追う |
| テーブル検索 | `getFieldsValue()` → `params` |
| ルート | `:id` / `?tab=` |
| インターセプタ | グローバルな `tenantId`、ページネーション、sign |
| enum select | `options` → API の enum 値 |

DevTools の call stack で `fetch`/`XHR.send` から上へ組み立て関数を追う。

#### 1b.3 — 組み立て三問（≠ Phase 2 認証三つのゲート）

| 問 | 答えるべきこと |
|---|---|
| **組み立て** | payload をどこで build するか、transform の痕跡 |
| **検証** | required、pattern、enum |
| **伝送** | path / query / body / multipart / ヘッダ |

インターセプタゲート（Phase 2）はついでにグローバル注入フィールド（Authorization、`X-Tenant-Id`、sign）を読む。

#### 1b.4 — Phase 3 との接続

候補フィールドは静的/バインディング層から得る。**必須/任意/条件依存**は Phase 3 のパラメータ行列 + diff + Phase 5 のエラー逆推定が必要。

---

## Phase 2 — 認証三つのゲート

`OUTDIR/js/` で grep（`head` 付き）し、`config.json` に書き込む（レシピは reference 参照）：

| ゲート | 問題 | キーワード |
|---|---|---|
| **レンダリングゲート** | ログイン済みかどうかをどう判定するか？ | `isLogin`、`getToken`、Cookie/localStorage |
| **インターセプタゲート** | 何が `/login` へのジャンプをトリガーするか？ | `response_code`、`errno`、axios interceptor |
| **コンテンツゲート** | メニュー/権限はどこから来るか？ | `menu`、`permission`、`role`、`acl`、`routes` |

localStorage のキー名を資格情報とみなすことを禁止する——chunk/リクエストチェーンから確認すること。

**出口 = ゲート B**：結論を `config.json` に落とし込み、`OUTDIR/runtime_harvest.js` / `preload.js` を修正する。

### Phase 2b — API 観察（任意）

OUTDIR 内の `preload.js` を使ってセッションキー名、Authorization、ネストした API URL を確認する：

| 設定 | 産出物 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers の観察 |
| `extractUrlsFromResponse: true` | レスポンス内の子 API |
| `observe.storageReads/cookieReads: true` | config への反映 |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage の各ラウンドでエクスポート：`__API_RECON_LOG__`、`__API_RECON_DETAIL__`、`__API_RECON_ROUTES__`、`__API_RECON_OBSERVE__`。

---

## Phase 3 — ランタイム

ゲート B を通過済みであること。[境界と禁止事項](#边界与禁止agent-必读--违反即越界) · 資格情報なし mock 戦略に従う。

`config.json` で `"runtimeMode": "depth" | "coverage" | "both"` を設定する（テンプレートは reference 参照）。

### Hook と stub（depth + coverage 共通）

| 層 | 範囲 | 目的 |
|---|---|---|
| L1 精密 | auth/権限/bootstrap stub | ファーストビューの認証を通過 |
| L2 負方向の修正 | すべての JSON レスポンス | 未ログインコード → 成功 |
| L3 フォールバック | L1 にヒットしない `/api` など | 空の成功ボディで UI を押し広げる |

- **depth**：fake auth + `forward` で業務コードを書き換え + `stubs`、`routes` を巡回（hash/history）、`runtime_api.json` を産出
- **coverage**：**document-start** で `preload.js` を注入（CDP `addScriptToEvaluateOnNewDocument` または Userscript）

検証：`window.__API_RECON_PRELOAD__` が存在する、業務 path が `/login` を返さない。

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage 動的列挙（必須）

1. メインナビ/サイドバー — 各項目をクリックし、ネットワークを 1–3s 待つ
2. Tab — `role=tab`、`.ant-tabs-tab`
3. テーブル — 先頭行の閲覧/編集/詳細
4. ツールバー — エクスポート、フィルタ、新規作成（**不可逆な削除を避ける**）
5. モジュールに入るたびに — API/ルートを統合
6. SPA — `routes.txt` が未カバーの path に対し、制御された `pushState`（MPA では禁止）

**パラメータトリガー行列**（必須）：各モジュールで操作タイプごとに1回ずつ記録し、**複数サンプルを diff** する：

| 操作 | 通常追加されるパラメータ |
|---|---|
| 一覧のファーストビュー | ページネーション + デフォルトフィルタ |
| 検索クリック | keyword、filter |
| 高度なフィルタ | さらに多くの optional |
| 新規作成/編集 | 完全な entity |
| 一括/エクスポート/ソート | `ids[]`、`exportType`、`sortField` |

**stub 下でも outbound の body/headers は本物**——リクエストを基準とする。記録 → `scan_raw.json`、`param_samples.json`、`api_detail.json`。

- **Vue**：`neutralizeVueRouter: true` + document-start preload
- **React**：`routes.txt` + サイドバークリック + `pushState`
- **both**：まず 3a depth、次に 3b coverage

---

## Phase 4 — 権限ツリー復元

**トリガー**：モジュールページが空白 / 各ルートが bootstrap のみ（例：locale）→ コンテンツゲート未通過。

| 現象 | 意味 |
|---|---|
| シェル進入成功 | レンダリングゲート + インターセプタゲート通過済み |
| サイドバーに項目が欠落/クリックしても空白 | stub の shape または権限コードが不完全 |
| 各ルートの API が同じで極端に少ない | `v-if permission` が通っていない |
| `routes.txt` が bundle より大幅に少ない | auth モジュールから補完する必要がある |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

典型的なチェーン：`role_permissions`（flat codes）+ `permissions/all`（tree）→ `getResultTree` → `userRouteAuth[CODE].url`。

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

中間産出物：`route_map.json`、`userRouteAuth.json`、`permissions_tree.json`、`*_stub.json`、`perm_codes_all.txt`。

stub チェック：外層の `response_code` がインターセプタゲートと一致、flat codes が tree と整合、`routes` が `route_map` の全 link をカバー。

`config.json` を更新したら**Phase 3 を再実行する**。大型 SPA では `waitUntil`、`routeTimeout`、`perRouteMs` を調整可能（reference A3/I節を参照）。

---

## Phase 5 — 統合とレポート

### 産出物表

| ファイル | 段階 | 内容 |
|---|---|---|
| `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt` | 1 | 静的 bundle と path |
| `param_candidates.json` | 1b | 静的なパラメータフィールド候補 |
| `config.json` | 2 | 三つのゲート + runtime 設定 |
| `runtime_api.json` | 3a | depth の詳細記録（WS/SSE を含む） |
| `param_samples.json`、`scan_raw.json`、`api_detail.json` | 3b | 複数サンプル、クリックログ、detail |
| `route_map.json` など | 4 | 権限ツリーの中間ファイル（実行した場合） |
| `params_merged.json` | 5 | 統合されたパラメータフィールド + 信頼度 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | ルート、API、params、機能ポイント、限界 |
| **insert_assets** | 5 | すべてのサービス、エンドポイント資産を資産ライブラリに書き込む |

### 5b — パラメータ統合

`param_samples.json` から diff する、**汎用の統合スクリプトなし**。信頼度ルールは reference J7 を参照（高/中/低/未トリガー）。

### 5c — エラー逆推定

認可範囲内で不完全なリクエストを送り、400 を読む（**パラメータ recon であり、脆弱性テストではない**）：`field 'x' is required`、enum エラーなど。`data` ラッパー、`variables`、暗号化前の `bizData` に注意。

レポートには明記すること：runtimeMode、静的/ランタイム API 数、パラメータ信頼度、未カバーモジュール、参考スクリプトに対する `CHANGES.md` の要約。

`site_map.json` の推奨構造：

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

さらなるフィールドと grep レシピは [reference.md](reference.md) を参照。

---

## 一般的な注意事項

- **フレームワーク非依存**：webpack/Vite/Angular の lazy load は手法が同じ
- **伝送**：REST/JSON、GraphQL、WebSocket、SSE、gRPC-web は範囲外
- **SSR**：クライアント側 fetch は記録可能、RSC/Server Actions は完全には列挙できない
- **盲点**：JSVMP、WASM、HMAC/mTLS の強い検証 → 静的 + 限界を明記
- **パラメータの盲点**：条件連動、hidden params、WASM 組み立て → 「未トリガー」/「到達不能」
- **静的はセーフティネット**：ランタイムが阻まれても静的解析は endpoint を列挙できる

---

## 追加リソース

- Grep レシピ、`config.json` テンプレート、トラブルシュート、Hook、パラメータ逆解析 J節、site_map テンプレート：**[reference.md](reference.md)**
- 参考スクリプトのパスは [スクリプトとゲート](#脚本与门禁) 表を参照
