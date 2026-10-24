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
Firestore に配信内容は置かない（最終更新IDのみ。正本は配信ログ）

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
import "tolo/operation/v1/operation.proto";

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

// 配信内容（oneof で型付け）
message Envelope {
  string delivery_id = 1;  // UUIDv7（resume に使用）
  google.protobuf.Timestamp sent_at = 2;
  tolo.operation.v1.DeliveryPayload payload = 3;  // DeliveryOrder の配信内容をそのまま返す
}
```

## 補足

- 配信内容の型は Operation の型（`tolo.operation.v1`）を参照し、本サービスでは定義しない。`realtime-delivery` のペイロード `DeliveryOrder` も Operation が定義し、本サービスは Operation の Go モジュールを import して復号する（ADR-0106）。Flow Control／Line Control の proto（`tolo.flow.v1`／`tolo.line.v1`）は import しない
  提案・行列案内は、観測が Flow／Line の応答から Operation の型へ変換したものである（Operation、Observation）
- 配信の流れ: Operation が DeliveryOrder を publish（delivery_id を採番）→ 本サービスが購読 → 配信ログへ永続化（delivery_id で重複排除）→ Firestore の最終更新ID更新 → リスナー検知 → FetchDeliveries → 未達分の返却
- FetchDeliveries を含む本サービスの RPC は、他サービス同様 Service Gateway 経由で提供する（明示した例外を除き Service Gateway を必ず経由する方針。context_map 等の「GW経由」表記と対応）
- Firestore はマネージドサービスの変更通知チャネルである（順応者に準じる外部依存）
  文書はイベント単位で最終更新IDのみを持つ。文書の粒度（イベント単位／ユーザ単位）の最適化は実装フェーズで検証する
- 配信ログは読み取りモデル（複製）であり、正本は Operation・観測側の永続化にある
  保持期間はテナント設定値とし、既定値は実装フェーズで検証する。超過時は全量再取得（参照系 API）へフォールバックする
- `realtime-delivery` は HTTP の push で受ける。受け口の形と応答の規約は service_transport.md の「情報更新 push の受け口」に従う
- 配信の再試行はブローカーに委譲する（at-least-once）。Operation が再試行するのは publish 失敗時だけである
  恒久的な失敗（復号できない、など）として受け取り済みにした配信は、上流から取り直す経路がなく、開いているスタッフアプリに届かない（ADR-0108）
  再配信による重複は delivery_id で排除する
- 取りこぼしは `resume_after_delivery_id` で回復する。ログ保持期間を超えた再開は全量再取得（参照系 API）にフォールバックする
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
- uid、最終更新 ID 文書のパス、`tenant_id` を照合するかどうかは実装フェーズで決める
