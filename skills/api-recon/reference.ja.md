# api-recon — リファレンスマニュアル

Grep レシピ、`config.json` テンプレートとトラブルシュート。すべての grep は `js/` ディレクトリに対して実行する。bundle が1行の場合は先に `js-beautify` または `sed 's/}/}\n/g'` を行ってもよいが、通常はコンテキスト窓付きの raw grep で十分である。

## スクリプトの説明

`scripts/` 内のすべてのファイルは**参考テンプレート**であり、実行前に必ず対象サイトに合わせて調整すること。典型的な修正ポイント：

| スクリプト | よくある調整項目 |
|---|---|
| `harvest_static.py` | endpoint 正規表現、webpack/Vite manifest 解析、マイクロフロントエンド publicPath、リトライ/並列 |
| `runtime_harvest.js` | neutralize のフィールド名と成功値、stub のマッチングルールと body 構造、routes の取得元、WS 記録、`waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`、L1 stubs、`neutralize.fields`、`apiPattern`、L3 を有効にするか、`recordDetail`、`observe.*`、`neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` 破壊的リンク、cookie、depth/max、同一ドメインフィルタ |
| `extract_route_map.py` | `routeMap` / `routeLink` 正規表現、KEY の命名パターン |
| `build_perm_tree.py` | `userRouteAuth` 解析、`ROOTS`/`PREFIX_PARENT` 階層ヒューリスティック、stub 外層のフィールド名 |
| `config.json` | 上記すべてのサイト固有パラメータの統一エントリー |

調整後のファイルはタスク作業ディレクトリ（例：`recon/`）に置くことを推奨し、レポートには参考スクリプトに対する具体的な変更を明記すること。

---

## A. 三つのゲートの逆解析

### A1. レンダリングゲート — 「ログイン済みかどうかをどう判定するか？」

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

チェーン `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` を見つけ、**ストレージキー**、**コンテナ**（Cookie vs localStorage）、**エンコーディング**を特定する：

| エンコーディング | config での偽造方法 |
|---|---|
| 平文文字列 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 署名なし/`alg:none` の JWT、または bundle 内の鍵で署名 |
| 暗号化（SM2/AES/RSA） | ハードコードされた鍵を探す、レンダリングゲートがデコード可能な blob のみを必要とする場合は forge 可能、そうでなければ静的フォールバック |

→ `cookies` / `localStorage` に書き込む。

### A2. インターセプタゲート — 「何が /login へのジャンプをトリガーするか？」

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

特定すること：**フィールド名**、**成功値**（通常 `0` または `200`）、**ジャンプをトリガーする失敗値**。junk セッションで検証する：

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ `neutralize.fields` + `neutralize.success` に書き込む。

### A3. コンテンツゲート — 「メニュー/権限はどこから来るか？」

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**二層データ**（企業向けバックオフィスでよくある）：

| API | 典型的な payload | 消費側 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | ルートガード、ボタンレベルの ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | サイドバーメニューのレンダリング |
| bundle 内 `userRouteAuth` | `{ CODE: { url, name? } }` | code → フロントエンド path |
| bundle 内 `routeMap` | `{ KEY: { name, link } }` | エイリアス解決（webpack `o.DASHBOARD`） |

消費側コードを読んで確認する：`getResultTree(tree, permissions)` がどうフィルタするか、`v-if` / `hasAuth(code)` がどのフィールドをチェックするか。

**手動 forge**（小規模サイト）：permissive な payload を構築 → `stubs`。

**完全な権限ツリー復元**（大規模サイト、サイドバー/子モジュールが依然として空白）：**I節**を参照。

---

## B. config.json テンプレート

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

フィールドの説明：
- `runtimeMode`：`depth`（Puppeteer）、`coverage`（browser MCP）、`both`
- `cookies[].value` のプレフィックス：`b64json:` → base64(JSON)、`json:` → 生の JSON、プレフィックスなし → リテラル
- `forward: true` は実際のリクエストを転送してコードフィールドを書き換える、`false` は完全にオフラインで stub
- `mockTier`：coverage モードの preload で有効にする層、例：`L1+L2`、`L1+L2+L3`
- `routes` は `routes.txt` 由来、メニューを forge した後 harness が自動的に `<a href>` を追加する
- `captureResponses` / `recordWs` は depth モードでのみ有効
- `waitUntil`：大型 SPA では `domcontentloaded` を使い、`networkidle2` によるハングを避ける
- `routeTimeout`：単一ルートの `page.goto` タイムアウト（ミリ秒）
- `proxy`：Puppeteer `--proxy-server`、`HTTP_PROXY` / `HTTPS_PROXY` でも設定可能

### B1. 二重 stub テンプレート（role_permissions + permissions/all）

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

外層のフィールド名（`response_code` / `code` / `data`）は A2 インターセプタゲートと一致させること、`permissions` は tree 内のすべての leaf code をカバーすること。

---

## C. coverage モード：preload 設定

`scripts/preload.js` 先頭の `CONFIG` オブジェクトを編集するか、CDP 注入前に置き換える：

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* config.json の stubs と同じ */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

検証：`window.__API_RECON_PRELOAD__ === true` かつ pathname が安定していること。

記録結果をエクスポート：

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook の機能

preload（coverage）と runtime_harvest（depth）に組み込まれたブラウザ Hook の機能とカバレッジ範囲：

| Hook 機能 | API 発見への価値 | カバレッジ |
|---|---|---|
| Hook fetch / XHR.open | リクエスト URL/メソッドを記録 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Authorization などのヘッダを発見 | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie 読み取り | セッションキー名を確認 | ⚠️ 任意 `observe.storageReads/cookieReads` |
| Vue のルート取得 | frontendRoutes を補完 | ✅ `__API_RECON_ROUTES__`（ロード済みルート） |
| Vue ルートガード中和 / ログインジャンプ阻止 | モジュールを押し広げて API をトリガー | ✅ `neutralizeVueRouter` + ネイティブジャンプ中和 |
| React のルート取得 | ルートを補完 | ⚠️ 静的 + クリック、専用 Hook なし |
| ページジャンプ阻止（ログイン path） | ページに留まって解析 | ⚠️ ログイン path のみ阻止、業務ナビゲーションを妨げない |
| Hook 暗号化ライブラリ（CryptoJS/SM など） | 暗号化パラメータ → 平文 API body | ❌ 暗号化関数の引数を手動 Hook する必要あり、結論を config に書く |
| アンチデバッグ bypass | さもなければ runtime で API を記録できない | ❌ 手動処理が必要、静的解析は依然として利用可能 |

---

## E. Endpoint 抽出正規表現（静的が少なすぎる場合）

`harvest_static.py` の `extract_endpoints` を緩めるか、手動で：

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. トラブルシュート

| 現象 | 原因 → 対処 |
|---|---|
| 静的 API が非常に少ない | endpoint の方言が合致しない → 正規表現を緩める（D節） |
| chunk 数 ≪ manifest | CSS-only または未デプロイの chunk、404 は再試行済み |
| runtime で依然としてログインページが表示される | レンダリングゲートの誤り → A1 を再確認：キー名、コンテナ、エンコーディング、domain |
| シェルに入ったがモジュールが空白 | コンテンツゲート → メニューを forge（A3）、`routes` の path が間違っている可能性 |
| 各ルートに bootstrap/locale しかない | 権限コードが不完全 → I節の権限ツリー復元、`role_permissions` + `permissions/all` の二重 stub をチェック |
| サイドバーに項目はあるが子ページが空白 | tree に中間ノードが欠落、または code が `userRouteAuth` と不一致 |
| すべての API がログインへジャンプ | インターセプタゲート → `neutralize` を確認、ネストしたフィールドは walk ロジックの拡張が必要 |
| WS フレームが 0 | ユーザー操作後に初めて subscribe する、`perRouteMs` を延ばす |
| レスポンスボディが空 | `forward: true` の場合のみ実際のレスポンスがある |
| Chromium が見つからない | chromium をインストールするか `config.chromium` / `CHROMIUM` を設定 |
| Mock が多いのに依然としてログインに戻る | Hook が遅すぎるか `location.href` setter が欠落 → document-start + preload |
| 一覧がすべて空 | L3 の空配列は正常、Tab/設定/詳細をクリックし続ける |
| Redux action をルートと誤認 | get/set/change/clear/toggle/upload を含む内部 path をフィルタする |
| Vue が依然としてログインへジャンプ | preload が document-start でない → 注入タイミングを修正、または `neutralizeVueRouter: false` の場合は手動でガードをクリア |
| レスポンスに URL があるが log に入らない | `extractUrlsFromResponse` を有効化、または `__API_RECON_DETAIL__` から手動抽出 |
| Authorization ヘッダ名が不明 | `observe.xhrHeaders` を有効化、または DevTools でリクエストヘッダを確認 |
| runtime が極端に遅い / タイムアウト | `waitUntil: domcontentloaded` に変更、`routeTimeout` を下げる、`networkidle2` を使わない |
| プロキシ接続失敗 | `proxy` / 環境変数をチェック、Puppeteer と curl のプロキシポートを一致させる |

---

## G. hardened な対象

サーバー側で段階的にセッションを検証する場合（forge 不可能な署名付き cookie、サーバー側レンダリングで stub 不可能なメニュー）、runtime は shell の段階で詰まる。想定される挙動：

- **静的解析だけで endpoint 列挙には十分** — モジュール path はコード内にある
- 認可が許す場合、**実際のセッション**で同じ harness を実行する：`forward: true`、neutralize 不要、実際の methods/params/responses をキャプチャ

---

## H. 単発タスクのチェックリスト

1. 認可範囲を確認
2. `scripts/harvest_static.py` を**読む** → 対象に合わせて調整 → 実行 → `api_static.txt`、`routes.txt` を精査
3. **Phase 1b**：path アンカー窓拡張 + バインディング層 → `param_candidates.json`（J節）
4. A1/A2/A3 を逆解析 → サイト固有の `config.json` を書く
5. `runtime_harvest.js` / `preload.js` を**読んで調整**してから実行
6. `runtimeMode=depth`：`npm install` → 調整後の harvest スクリプトを実行
7. `runtimeMode=coverage/both`：document-start で調整後の preload を注入 → browser MCP 動的列挙 + **パラメータトリガー行列**
8. モジュールがレンダリングされない → **I節の権限ツリー復元** → patch stubs → 再実行
9. パラメータの複数サンプル diff + エラー逆推定 → `params_merged.json`
10. 統合 → `site_map.json` + `api_merged.txt`、カバレッジ・ギャップ・スクリプト変更点を正直に明記

---

## I. 権限ツリー復元（Phase 4 深掘り）

単純な `menus: [{ path, show: true }]` の forge が効かず、子モジュールが依然としてマウントされない場合に使う。

### I1. auth モジュールを特定

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

記録すること：**権限 API path**、**レスポンスフィールド名**、**消費する chunk ファイル名**。

### I2. routeMap を抽出

```bash
python3 scripts/extract_route_map.py recon/js recon/
# recon/route_map.json を産出
```

`[!] no routeMap pattern found` の場合：`extract_route_map.py` 内の正規表現を緩めるか、手動で grep：

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 権限ツリー + stub を構築

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

スクリプトのロジック：
1. `userRouteAuth={MONITOR:{url:...},...}` を解析（webpack エイリアス `He=o.DASHBOARD` を含む）
2. `route_map.json` を使って alias → 実際の path を解決
3. code プレフィックスから parent を推論（`MONITOR_ALERT` → `MONITOR`）
4. `permissions_tree.json`、`permissions_all_stub.json`、`role_permissions_stub.json` を出力
5. `--config` 指定時は `config.json` の `stubs` と拡張された `routes` に自動書き込み

**対象に合わせて調整**（スクリプト先頭）：
- `DEFAULT_ROOTS`：トップレベルモジュールの code リスト
- `DEFAULT_PREFIX_PARENT`：`PREFIX_` → parent マッピング
- `DEFAULT_EXTRA_PARENT`：プレフィックス関係にない orphan ノード

### I4. stub の整合性を検証

```bash
# permissions の数は userRouteAuth の項目数 ≈ であるべき
wc -l recon/perm_codes_all.txt
# routes は route_map の全 link をカバーすべき
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. runtime を再実行して比較

```bash
node recon/runtime_harvest.js recon/config.json
# forge 前後の runtime_api.json の件数を比較、/attack、/asset などでモジュール API が現れるか確認
```

| forge 前 | forge 後（成功） |
|---|---|
| 各ルートで同じ 3–5 件の bootstrap | ルートごとに異なる module API がトリガーされる |
| `/api/locale/language` のみ | `/api/web/...` モジュール endpoint が現れる |
| `routes.txt` が一桁のルート | `routes` が route_map 由来で 80–110+ |

### I6. それでも失敗する場合

- **coverage モード**：サイドバー + Tab をクリック、権限 gating は操作後に初めてリクエストされる可能性
- **stub フィールド**：実際の API（curl + 実際の session）と stub の nesting を比較
- **追加のガード**：`hasPermission|checkRole|func.` などのボタンレベルチェックを grep し、`role_permissions.permissions` を拡張
- **静的フォールバック**：モジュール API path は依然として `api_static.txt` にある、runtime は METHOD/body の補完のみ、パラメータは `param_candidates.json` + 記録済みサンプルを保持

---

## J. パラメータ逆解析（Phase 1b / 5b / 5c）

**方法論であり、汎用スクリプトではない。** path を探すには正規表現、パラメータを探すにはアンカー窓拡張 + UIバインディングチェーン + 複数サンプル diff + エラー逆推定。

### J1. アンカー窓拡張 — path から組み立てオブジェクトを探す

```bash
# Phase 1 で既知の path をアンカーとする
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. ラッパー層と伝送形態

```bash
# axios / 統一 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# パスパラメータ
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 検証ゲート — 必須 / 形式 / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. バインディング層 — フォーム → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime での補完：DevTools → Network → リクエスト → **イニシエータ**（call stack）で `fetch`/`send` から上へ組み立て関数を追う。

### J5. 暗号化パラメータ

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**暗号文の上でフィールドを推測しない** — 暗号化関数の**引数**を Hook し、暗号化前の plaintext payload を記録する、結論を `config.json` / `param_candidates.json` に書く。

### J6. パラメータトリガー行列（Phase 3 必須）

各モジュールで操作ごとに1回ずつ記録し、リクエストの body/query を diff する：

| 操作 | 注目点 |
|---|---|
| 一覧のファーストビュー | ページネーションのデフォルト値 |
| 検索 | keyword、filters |
| 高度なフィルタ | optional フィールド |
| 新規作成/編集 | 完全な entity |
| 一括/エクスポート | `ids[]`、`exportType` |
| ソート/ページ送り | `sortField`、`order` |

`param_samples.json` を産出：`[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 信頼度ルール

| 信頼度 | 条件 |
|---|---|
| **高** | 静的 callsite + runtime ≥2 サンプルが一致 |
| **中** | 静的のみ、または runtime 1回のみ |
| **低** | レスポンス/エラーからの逆推定、二次検証なし |
| **未トリガー** | 静的に既知のフィールド、UI/権限が到達していない |

### J8. シナリオ別クイック設定

| シナリオ | 順序 |
|---|---|
| REST 一覧ページ | J1 組み立てオブジェクト → J6 4回 diff → J3 rules |
| 新規作成/編集フォーム | J3 Form name → J4 submit チェーン → runtime で送信 + 故意に空欄にして 400 を見る |
| GraphQL | J2 variables 宣言 → runtime で各 operation の variables を記録 |
| 暗号化 body | J5 引数を Hook → 暗号化前のフィールドが真の params |

### J9. api-recon の各段階とのマッピング

| api-recon | パラメータ recon |
|---|---|
| Phase 1 静的 | J1 アンカー窓拡張 |
| Phase 2 A2 インターセプタ | グローバル注入フィールド（tenantId、sign） |
| Phase 3 runtime | J6 トリガー行列 + `param_samples.json` |
| Phase 4 権限ツリー | モジュールごとにフォームが異なる → 権限が足りて初めて全フィールドがトリガーされる |
| Phase 5 統合 | `params_merged.json` + 信頼度、単一サンプルで必須を確定しない |

### J10. トラブルシュート

| 現象 | 対処 |
|---|---|
| 静的にフィールド名があるが runtime で一度も現れない | 「未トリガー」と明記、権限ツリーを補完 / 高度なフィルタをクリック / 連動 select の各 option |
| 同一 path で body の形状が異なる | 正常 — `action` ごとに個別に記録、schema を無理に統合しない |
| stub レスポンスは偽だが params を見たい | **outbound リクエスト**の body/headers を見る、stub レスポンスから逆推定しない |
| 400 で nested field が報告される | 外層ラッパー `data`/`bizData`/`variables` に注意 |
| GraphQL で operation 名しか見えない | `variables` JSON を展開、静的に `$var: Type` を探す |

---
