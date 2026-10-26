# tolo-realtime

## 開発

### セットアップ

mise を使用して開発環境をセットアップする  
`mise install` の後処理で lefthook の pre-commit フック（シークレット検出と golangci-lint）が導入される

```bash
mise trust
mise install
cp .env.example .env
task proto
```

`.env` は Compose と `task migrate:*` が開発用 PostgreSQL の接続情報（`POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`）を読む先で、git 管理外

### テストと lint

```bash
go build ./...
go test ./...
golangci-lint run
```

`go test ./...` は統合テストで PostgreSQL コンテナを起動するため、Docker が必要

### e2e テスト

```bash
task e2e
```

Docker Compose で PostgreSQL、Firebase のエミュレータ、サーバー、e2e テストのコンテナを起動し、e2e テストの終了コードで終わる。終了後に `docker compose --profile e2e down -v` で片付ける  
e2e テストのコンテナは内部 JWT の JWKS を `:8080` で配信し、サーバーはそれを `INTERNAL_JWKS_URL=http://e2e:8080` で参照する  
push の受け口に `DeliveryOrder` を送り、配信ログ、Firestore の最終更新 ID 文書、`FetchDeliveries`、`IssueFirestoreToken` のカスタムトークンとセキュリティルールを確かめる

### Docker Compose での起動

Docker Compose で PostgreSQL、golang-migrate によるマイグレーション、Firebase のエミュレータ、および開発サーバーを起動できる

```bash
docker compose up --build
```

もしくは
```bash
task up:build
```

サーバーは `http://localhost:8080`、PostgreSQL は `localhost:5432`、`mockdeps` は `http://localhost:8089` で待ち受ける（停止は `task down`）  
Firebase のエミュレータはこのリポジトリの `firebase-emulator/` からビルドする。`firestore.rules` には Realtime の最終更新 ID 文書（`deliveryNotifications/{eventId}`）のルールだけを置く。ホストへは Firestore を `localhost:8081`、Firebase Auth を `localhost:9099`、Emulator UI を `http://localhost:4000` で公開する  
Compose の `migrate` サービスは `Dockerfile` の `migrate` ターゲットをビルドして起動するため、`migrations/` を追加・変更したあとは `--build` 付き（`docker compose up --build` または `task up:build`）で起動し直す  
`task up` はイメージを再ビルドしないので、古いマイグレーションのままになる  
RPC を呼び出すには Service Gateway 発行の内部 JWT が必要である。Compose では `mockdeps` が Service Gateway の代わりに JWKS を配信し、トークンを発行する（「内部 JWT」を参照）

### 環境変数

| 環境変数 | デフォルト | 内容 |
| --- | --- | --- |
| `SERVER_ADDR` | `:8080` | 待ち受けるアドレス |
| `DATABASE_URL` | なし（必須） | PostgreSQL の接続先 |
| `FIRESTORE_PROJECT_ID` | なし（必須） | 最終更新 ID 文書を書く Firestore と、カスタムトークンを発行する Firebase Auth のプロジェクト |
| `FIRESTORE_EMULATOR_HOST` / `FIREBASE_AUTH_EMULATOR_HOST` | なし | 設定すると Firestore と Firebase Auth のエミュレータに接続する。Compose では `firebase-emulator:8080` と `firebase-emulator:9099` |
| `INTERNAL_JWKS_URL` | `http://gateway:8080/.well-known/jwks.json` | 内部 JWT の検証に使う JWKS の取得先（Compose では `mockdeps` の `http://mockdeps:8080/.well-known/jwks.json`） |
| `INTERNAL_JWT_ISSUER` | `service-gateway` | 内部 JWT に期待する `iss` |
| `INTERNAL_JWT_AUDIENCE` | `tolo-realtime` | 内部 JWT に期待する `aud` |
| `LOG_LEVEL` | `info` | ログに出力する最小レベル。`debug`／`info`／`warn`（`warning` も同義）／`error`／`critical` を取り、未知の値ならサーバーは起動しない |
| `GOOGLE_CLOUD_PROJECT` | なし | 設定するとログの `logging.googleapis.com/trace` を `projects/<project>/traces/<trace_id>` 形式にし、Cloud Logging でトレースと相関させる。未設定なら素のトレース ID を出力する |
| `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | なし | どちらかを設定したときだけ OTLP/HTTP で span を export する。未設定ならトレーシングは無効 |
| `OTEL_SERVICE_NAME` | `tolo-realtime` | トレースの `service.name` |

ヘッダ・TLS・タイムアウトなどその他の `OTEL_EXPORTER_OTLP_*` は exporter がそのまま解釈する  
ログは標準出力へ 1 行 1 件の JSON で書き出す

### push の受け口

`realtime-delivery` の push は `POST /push/realtime-delivery` で受ける。Service Gateway を経由せず、内部 JWT も要求しない。到達を許す呼び出し元の制限はプラットフォームに任せる  
本文は `tolo.operation.v1.DeliveryOrder` のバイト列で、配信ログに追記したうえで、Firestore の `deliveryNotifications/{event_id}` の `latestDeliveryId` をそのイベントの最後に届いた `delivery_id` に書き換える。同じ `delivery_id` の再配信は配信ログに追記せず、Firestore だけを書き直す  
応答は、処理できたときと、復号できない本文を捨てたときが 204、配信ログか Firestore に書けなかったときが 503 である

### マイグレーション

ローカルでマイグレーションを実行する場合は、Compose で PostgreSQL を起動してから次を実行する（接続先は `DATABASE_URL` で上書きできる）  
`task migrate:*` が使う golang-migrate CLI は `.config/mise/conf.d/db.toml` で管理しており、`mise install` で `mise.toml` のツールと一緒に導入される

```bash
# マイグレーションを適用
task migrate:up
# 新しいマイグレーションを作成（up/down のペアを生成）
task migrate:create -- <migration_name>
# 1 つ前にロールバック
task migrate:down
# 現在のバージョンと dirty 状態を表示
task migrate:version
```

### トレースの確認（Jaeger）

監視スタックはオーバーライドファイル `compose.o11y.yml` を重ねたときだけ有効になる  
Jaeger が起動し、`server` に OTLP エクスポート用の環境変数（`OTEL_EXPORTER_OTLP_ENDPOINT` など）がセットされる

```bash
docker compose -f compose.yml -f compose.o11y.yml up --build
```

もしくは
```bash
task up:build:o11y
```

Jaeger UI は `http://localhost:16686`（停止は `task down:o11y`）

### 内部 JWT

RPC を呼び出すには Service Gateway 発行の内部 JWT が必要である  
Compose で起動した場合は、`mockdeps` の `POST /token` でトークンを発行する。`server` は `mockdeps` の JWKS で検証するので、Service Gateway を経由せずに `Authorization: Bearer <token>` で呼び出せる  
発行も検証も HTTP だけで済むので、Go 以外の言語のクライアントからも使える

```bash
TOKEN=$(curl -s -X POST localhost:8089/token \
  -d '{"tenant_id":"0123456789abcdef","event_id":"fedcba9876543210","subject":"staff-user-1"}' \
  | jq -r .token)
```

- 入力の JSON は `audience`（既定 `tolo-realtime`）、`token_use`（既定 `event_access`）、`tenant_id`、`event_id`、`subject`、`scope`（空白区切り、既定 `events.read`）、`ttl`（Go の duration 形式、既定 `10m`）を取る
- 本サービスの RPC は `event_access` と `events.read` を要求する。`tenant_id` と `event_id`（どちらも 16 文字 hex）は必須である
- `subject` はトークンの `sub` で、本サービスはこれをスタッフの user_id として扱う。配信は `recipient_user_ids` にこの値を含むものだけが返る
- `iss` は `service-gateway` に固定される
- 応答は `token` / `claims` / `jwks` を持つ JSON である
- 鍵は `mockdeps` を起動するたびに作り直されるので、再起動の後はトークンを発行し直す

Compose を使わずにトークンを作る場合は、`internal-jwt-handling` 同梱の jwtgen CLI を `go tool jwtgen` で実行する（`go.mod` の `tool` に登録してある）  
出力の `jwks` を任意の HTTP エンドポイントで配信し、`INTERNAL_JWKS_URL` にその URL を設定する

```bash
go tool jwtgen -audience tolo-realtime -token-use event_access -tenant-public-id 0123456789abcdef -event-public-id fedcba9876543210 -subject staff-user-1 -scope events.read -ttl 10m
```

### クライアントからの接続

サーバーは Connect と gRPC を同じポートで受ける。gRPC は TLS なしの HTTP/2（h2c）で受けるので、開発中のクライアントは `http://localhost:8080` へ gRPC で直接つなげる  
次の例は、import する proto まで含めたアーティファクト（「proto アーティファクトの利用」を参照）を `proto` に展開し、`buf curl` で gRPC として呼び出す

```bash
buf curl --schema proto --protocol grpc --http2-prior-knowledge \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"event_id":"fedcba9876543210"}' \
  http://localhost:8080/tolo.realtime.v1.RealtimeFetchService/FetchDeliveries
```

配信ログが空のうちは `FetchDeliveries` が何も返さない。`POST /push/realtime-delivery` に `DeliveryOrder` を送ると、テスト用の配信を入れられる  
`delivery_id` は UUIDv7 でなければならない。`/push/realtime-delivery` は復号できない本文も 204 で受け取って捨てるので、入らないときは `server` のログを確かめる

```bash
echo '{"delivery_id":"019a1f00-0000-7000-8000-000000000001","event_id":"fedcba9876543210","recipient_user_ids":["staff-user-1"],"payload":{"staff_message":{"body":"hello"}}}' \
  | buf convert proto --type tolo.operation.v1.DeliveryOrder --from -#format=json --to -#format=binpb \
  | curl --data-binary @- localhost:8080/push/realtime-delivery
```

### connect-es の生成

connect-es の生成（`task proto:gen:es`）はリリース時に CI で行う  
ローカルで実行する場合は `clients/connect-es` の依存（`npm i`）を導入する必要がある

## イメージからマイグレーションを実行する

`Dockerfile` の `migrate` ターゲットは golang-migrate CLI のイメージに `migrations/` を `/migrations` として同梱したものである  
リポジトリを clone しなくても、このイメージだけで DB マイグレーションを実行できる  
`ENTRYPOINT` は `migrate -path /migrations` なので、利用者は接続先とコマンドだけを引数として渡す

ローカルでビルドする場合は次のとおりである

```bash
docker build --target migrate -t tolo-realtime-migrate .
```

適用は `-database` に接続先を、続けてコマンドを渡す

```bash
docker run --rm --network host tolo-realtime-migrate -database "$DATABASE_URL" up
```

公開イメージは `ghcr.io/<owner>/<repo>-migrate` で、サーバーのイメージと同じバージョンタグを付ける

```bash
docker run --rm --network host ghcr.io/<owner>/<repo>-migrate:<version> -database "$DATABASE_URL" up
```

そのほかのコマンドも同じ形で渡す

```bash
# 現在のバージョンを確認
docker run --rm --network host ghcr.io/<owner>/<repo>-migrate:<version> -database "$DATABASE_URL" version
# 1 つ前にロールバック
docker run --rm --network host ghcr.io/<owner>/<repo>-migrate:<version> -database "$DATABASE_URL" down 1
```

golang-migrate CLI は接続先を環境変数からは読まないため、`-database` は必ず引数で渡す  
`--network host` はコンテナからホスト上の PostgreSQL に接続するための指定であり、接続先がホスト外にあるなら不要である

## proto アーティファクトの利用

`.proto` は [ORAS](https://oras.land) で OCI アーティファクト化され、GitHub Container Registry に公開される  
アーティファクトは 2 つあり、同じバージョンタグで公開する

| アーティファクト名 | 中身 | `task proto:export` の書き出し先 |
| --- | --- | --- |
| `ghcr.io/<owner>/<repo>-proto` | `tolo/realtime/v1/realtime.proto` だけ | `build/proto` |
| `ghcr.io/<owner>/<repo>-proto-bundle` | `tolo/realtime/v1/realtime.proto` と、それが import する proto（`tolo/operation/v1/operation.proto`、`tolo/kernel/v1/kernel.proto`、`authz/v1/options.proto`）をすべて | `build/proto-bundle` |

`-proto` だけでは import を解決できないので、`buf` や `protoc` の入力にするには operation、kernel、authz の proto を別に用意する  
`-proto-bundle` はそれだけで入力がそろうので、Go を使わない言語のクライアントもそのまま生成に使える

### 取得（pull）

[ORAS CLI](https://oras.land/docs/installation) が必要（mise でも導入される）

```bash
# 出力先ディレクトリに proto を展開（ディレクトリ構造が復元される）
oras pull ghcr.io/<owner>/<repo>-proto:latest -o proto
# import する proto まで含めて展開する
oras pull ghcr.io/<owner>/<repo>-proto-bundle:latest -o proto
```
