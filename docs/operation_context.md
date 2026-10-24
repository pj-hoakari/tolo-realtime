# スタッフ間コミュニケーションコンテキスト 入出力整理

本コンテキストは1つだが、デプロイ単位は Operation・Realtime・Notification の3つに分かれる。
配送の支援機構（Realtime／Notification）を独立コンテキストにはしないが、デプロイ単位としては明確に切り分ける。配送経路ごとに可用性の要求と外部依存（Firestore・プッシュ基盤）が異なるためである。

ドメイン定義: operation_domain.md
デプロイ単位: Operation、realtime_spec.md、Notification
（Realtime／Notification は支援機構で独立コンテキストにしない）

## ドメインイベント↔RPC 対応

| 区分 | ドメインイベント | 対応 |
|---|---|---|
| スタッフ↔スタッフ | StaffMessageSent | Operation: StaffCommunicationService.SendStaffMessage |
| スタッフ↔スタッフ | StaffMessageRevoked | Operation: StaffCommunicationService.RevokeStaffMessage（取り下げは `guest-messages` の revoked_message_ids で伝播） |
| スタッフ↔スタッフ | GuidanceInfoShared | Operation: StaffCommunicationService.ShareGuidanceInfo |
| 運用操作 | ReassignmentDirected | Operation: OperationControlService.DirectReassignment |
| 運用操作 | ReassignmentApplied | Operation: OperationControlService.ApplyReassignment |
| 接続状態 | AppConnected／AppDisconnected | スタッフアプリ → Operation: DeliveryCoordinationService.UpdateConnectionState（アプリが直接通知。Realtime は持続接続を持たないため接続検知を担わない） |
| 配送 | DeliveryRouted | Operation: DeliveryCoordinationService.RequestProposalDelivery（観測から） |
| 配送 | RealtimeDelivered | Operation が `realtime-delivery` トピックへ publish（PubSub）→ Realtime が購読して配信ログへ永続化 |
| 配送 | PushNotificationSent／PushNotificationFailed | Notification: NotificationService.SendPush |
| テナント内集約 | HistoryDigestAggregated | Operation 内部バッチ（参照は DigestService.GetHistoryDigest） |
| テナント内集約 | FeedbackAccumulated | Operation: DeliveryCoordinationService.RecordFeedbackValues（観測から） |
| テナント内集約 | FeedbackAnonymized | Operation 内部バッチ（→ RefAgg: SubmitAnonymizedFeedback へ投入） |

手動介入（危険フラグ等）と運用状態の変更（ゲート開閉 GateOpened／GateClosed・観測点設定変更）は誘導への介入として誘導ドメインに属し、本コンテキストの外にある（受け口は 観測コンテキスト）

## 他コンテキストとの接点

| 接点 | 対応 |
|---|---|
| 観測からの依頼（顧客・供給者。観測が顧客） | 受: RequestProposalDelivery／RecordFeedbackValues。本コンテキストから観測への同期 RPC はない。提案・行列案内・フィードバック値は本コンテキストの型で受け、観測が腐敗防止層として Flow／Line の型から変換する。Realtime の配信内容も本コンテキストの型を参照する |
| 匿名化済みフィードバックの供給（保護境界外へ） | ReferenceAggregationService.SubmitAnonymizedFeedback（参照値集約コンテキスト） |
| ゲスト向けメッセージの供給 | `guest-messages` トピックへ publish（PubSub。Guest Service が購読）。復旧時のみ ListStaffMessages を pull |
