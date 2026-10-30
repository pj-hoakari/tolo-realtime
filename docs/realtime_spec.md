# Realtime 入出力仕様

package: `tolo.realtime.v1`
実現するコンテキスト: operation_context.md（支援機構。独立コンテキストにしない）
役割: 開いているスタッフアプリへのリアルタイム配信
イベントバスではなく、クライアント向けの配送チャネルである

設計方針:
配信内容は Operation が `realtime-delivery` トピックへ publish し、本サービスが購読して自 DB の配信ログに永続化する（PubSub 経由）
クライアントは pull（FetchDeliveries）で取得する
変更通知には Firestore を使う。イベント単位の文書に最終更新ID（UUIDv7。タイムスタンプ順序特性を持つ）のみを保持し、
スタッフアプリはリアルタイムリスナーで変更を検知してイベントドリブンに pull する
Firestore が使えない環境や障害時は、ロングポーリング（`long_poll_timeout_seconds`）にフォールバックする
Firestore に配信内容は置かない（最終更新IDのみ。配信内容は配信ログが持つ）

## 入出力一覧

| 入出力 | 説明（ユビキタス言語） | 相手 | 認可・配信 | 関連ドメインイベント |
|---|---|---|---|---|
| `realtime-delivery` トピック購読 | Operation が publish した配信内容（`tolo.operation.v1.DeliveryOrder`）を HTTP の push で受信し、配信ログへ永続化（delivery_id で重複排除）して Firestore の最終更新IDを書き換える | publish 元: Operation | PubSub（at-least-once、イベント単位の順序キー） | RealtimeDelivered |
| FetchDeliveries | 配信ログから `resume_after_delivery_id` より後のエントリを返す。`long_poll_timeout_seconds` 指定時は新着があるまで待機する（ロングポーリング） | スタッフアプリ | `event_access` + events.read | （参照。取得の起点は Firestore リスナーの変更検知） |
| IssueFirestoreToken | Firestore の変更検知（最終更新 ID 文書）をリアルタイムリスナーで読むための Firebase Auth カスタムトークンを発行する。クレームと期限は「カスタムトークン（確定分）」のとおり | スタッフアプリ | `event_access` + events.read | （発行のみ） |

接続状態（開／閉）はスタッフアプリが Operation の `UpdateConnectionState` へ直接通知する（フォアグラウンド／バックグラウンド遷移時）
本サービスは持続接続を持たないため接続検知を担わない（最終 fetch 時刻による閉判定の補助は実装フェーズで検討）

## 参考 proto 定義

```proto
syntax = "proto3";
package tolo.realtime.v1;
import "google/protobuf/timestamp.proto";

service RealtimeFetchService {
  // スタッフアプリ → 本サービス（pull。起点は Firestore リスナーの変更検知、フォールバックはロングポーリング）
  rpc FetchDeliveries(FetchDeliveriesRequest) returns (FetchDeliveriesResponse);
  // Firestore の変更検知を読むためのカスタムトークン発行（アクセス制御はセキュリティルールが担う）
  rpc IssueFirestoreToken(IssueFirestoreTokenRequest) returns (IssueFirestoreTokenResponse);
}

message IssueFirestoreTokenRequest {
  string event_id = 1;
}
message IssueFirestoreTokenResponse {
  string firebase_custom_token = 1;  // 短命。Firebase Auth の signInWithCustomToken に渡す
  google.protobuf.Timestamp expires_at = 2;  // クレームの expires_at と同じ時刻
}

message FetchDeliveriesRequest {
  string event_id = 1;
  optional string resume_after_delivery_id = 2;   // この ID より後を返す。未指定は直近ログ（本数は実装フェーズで決定）
  optional uint32 long_poll_timeout_seconds = 3;  // 指定時は新着があるまで待機（フォールバック用）
}
message FetchDeliveriesResponse {
  repeated Envelope envelopes = 1;  // 呼び出し元が宛先に含まれるもののみ
  string latest_delivery_id = 2;    // Firestore の最終更新IDと対応
}

// 配信内容
message Envelope {
  string delivery_id = 1;  // UUIDv7（resume に使用）
  google.protobuf.Timestamp sent_at = 2;
  bytes payload = 3;  // tolo.operation.v1.DeliveryPayload を符号化したバイト列。DeliveryOrder の配信内容をそのまま返す
}
```

## 補足

- 配信内容の型は Operation の `tolo.operation.v1.DeliveryPayload` であり、本サービスでは定義しない。`FetchDeliveries` はこれを符号化したバイト列で返し、本サービスの proto は Operation の proto を import しない。スタッフアプリは Operation の proto から生成した型で復号する（ADR-0114）
  `realtime-delivery` のペイロード `DeliveryOrder` も Operation が定義し、本サービスは Operation の Go モジュールを import して復号する（ADR-0106）。Flow Control／Line Control の proto（`tolo.flow.v1`／`tolo.line.v1`）は import しない
  提案・行列案内は、観測が Flow／Line の応答から Operation の型へ変換したものである（Operation、Observation）
- 配信の流れ: Operation が DeliveryOrder を publish（delivery_id を採番）→ 本サービスが購読 → 配信ログへ永続化（delivery_id で重複排除）→ Firestore の最終更新ID更新 → リスナー検知 → FetchDeliveries → 未達分の返却
- FetchDeliveries を含む本サービスの RPC は、他サービス同様 Service Gateway 経由で提供する（明示した例外を除き Service Gateway を必ず経由する方針。context_map 等の「GW経由」表記と対応）
- Firestore はマネージドサービスの変更通知チャネルである（順応者に準じる外部依存）
  文書はイベント単位で最終更新IDのみを持つ。文書の粒度（イベント単位／ユーザ単位）の最適化は実装フェーズで検証する
  文書のパスは `deliveryNotifications/{event_id}`、フィールドは `latestDeliveryId`（文字列）とする。`DeliveryOrder` はテナントを持たないので、パスにテナントを含めない
  `latestDeliveryId` は、そのイベントで最後に配信ログへ届いた配信の `delivery_id` である。再配信を受けたときも、配信ログの最後の配信で書き直す。Firestore への書き込みに失敗した push は一時的な失敗とし、再配信で書き直す
- 配信ログは読み取りモデル（複製）であり、元のデータは Operation・観測側の永続化が所有する
  保持期間はテナント設定値とし、既定値は実装フェーズで検証する。超過時は全量再取得（参照系 API）へフォールバックする
- `realtime-delivery` は HTTP の push で `POST /push/realtime-delivery` に受ける。受け口の形と応答の規約は service_transport.md の「情報更新 push の受け口」に従う
  本サービスの受け口が恒久的な失敗として扱うのは、ペイロードを復号できない場合だけである。`delivery_id` が UUIDv7 でない、`event_id` が空、宛先がない、`payload` がない `DeliveryOrder` も、復号できないものとして扱う。本サービスはイベントの存在を知らないので、対象のイベントが存在しないことでは失敗としない
- 配信の再試行はブローカーに委譲する（at-least-once）。Operation が再試行するのは publish 失敗時だけである
  恒久的な失敗（復号できない、など）として受け取り済みにした配信は、上流から取り直す経路がなく、開いているスタッフアプリに届かない（ADR-0108）
  再配信による重複は delivery_id で排除する
- 取りこぼしは `resume_after_delivery_id` で回復する。ログ保持期間を超えた再開は全量再取得（参照系 API）にフォールバックする
  配信ログの順序は、本サービスに届いた順とする。UUIDv7 の `delivery_id` は Operation が採番した時刻の順で、届く順と食い違うことがあるので、順序には使わない
  FetchDeliveries は、`resume_after_delivery_id` の配信より後に届いた配信を、届いた順に最大 100 件返す。`resume_after_delivery_id` が未指定なら、呼び出し元が宛先に含まれる直近の 100 件を届いた順に返す
  `latest_delivery_id` は、次の呼び出しで `resume_after_delivery_id` に渡す値である。100 件で打ち切ったときは返した最後の配信、そうでなければそのイベントで最後に届いた配信（宛先を問わない）の `delivery_id` を返す。配信がひとつもなければ空文字を返す
  `resume_after_delivery_id` がそのイベントの配信ログにないときは `OUT_OF_RANGE` を返す。スタッフアプリは全量再取得にフォールバックする
  `long_poll_timeout_seconds` を指定し、返す配信がないときは、配信が届くか指定の秒数（上限 30 秒）が過ぎるまで待つ
- 配信対象の選別（誰が開いているか）は Operation の接続状態にもとづく。本サービスは依頼された宛先分をログに残し、fetch 時に呼び出し元の宛先分に絞る
- Firestore のアクセス制御はカスタムトークン方式とする
  スタッフアプリは IssueFirestoreToken で得たカスタムトークンで Firebase Auth にサインインし、リスナーを張る
  クレーム、期限、セキュリティルールは「カスタムトークン（確定分）」のとおりとする
  WebRTC シグナリングの Firestore アクセス制御も同一方式で統一する（発行は Edge Bridge Service。文書の構造とルールは Edge Bridge Service）

## カスタムトークン（確定分）

決定の根拠は ADR-0098 にある。

- カスタムクレームは次のとおりとする

| クレーム | 値 |
|---|---|
| `event_id` | リクエストの `event_id`（Service Gateway が `event_access` と突き合わせた値） |
| `token_use` | `delivery_notification`。Firestore 用カスタムトークンの用途を表す。内部 JWT の `token_use` とは別の語彙である |
| `expires_at` | `event_access` の `exp`（UNIX 時刻、秒）。Firestore へアクセスできる期限 |

- Firebase のセッションはリフレッシュトークンで更新され続けるため、カスタムトークンの有効期間（発行から 1 時間）は Firestore へアクセスできる期間の上限にならない。上限は `expires_at` とし、セキュリティルールで打ち切る
- レスポンスの `expires_at` はクレームの `expires_at` と同じ時刻を指す。スタッフアプリは、この時刻の前に新しい `event_access` でカスタムトークンを取り直し、サインインし直す
- セキュリティルールは、クレームの `event_id` が文書パスと一致すること、`token_use` が `delivery_notification` であること、`request.time` が `expires_at` より前であることを条件に、該当イベントの最終更新 ID 文書の読み取りだけを許す。書き込めるのは本サービス（Admin SDK）だけである
- `token_use` を照合するので、Edge Bridge Service のカスタムトークン（`event_id` を持つ）では最終更新 ID 文書を読めない
- `expires_at` は、内部 JWT の `src_exp`（Service Gateway が `event_access` の `exp` を写したもの）から取る。内部 JWT の `exp` は `event_access` の `exp` より早く切れるので使わない（ADR-0109）
- uid は内部 JWT の `sub`（スタッフの user_id）とする
- 最終更新 ID 文書のパスは `deliveryNotifications/{event_id}` とする。`tenant_id` はクレームに載せず、照合しない。`event_id` はテナントをまたいで重ならない公開 ID であり、`DeliveryOrder` がテナントを持たないためである
