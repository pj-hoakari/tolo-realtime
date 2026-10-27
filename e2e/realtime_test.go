//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	firebase "firebase.google.com/go/v4"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"
	"github.com/pj-hoakari/internal-jwt-handling/jwtgen"

	operationv1 "github.com/pj-hoakari/tolo-operation/gen/tolo/operation/v1"
	realtimev1 "github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1"
	"github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1/realtimev1connect"
)

const (
	serverURL    = "http://server:8080"
	firestoreURL = "http://firebase-emulator:8080/v1/projects/demo-tolo/databases/(default)/documents"
	authURL      = "http://firebase-emulator:9099/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=e2e"
	jwksAddr     = ":8080"
)

var (
	generator *jwtgen.Generator
	client    realtimev1connect.RealtimeFetchServiceClient
)

func TestMain(m *testing.M) {
	var err error

	generator, err = jwtgen.NewGenerator("e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create JWT generator:", err)
		os.Exit(1)
	}

	go func() {
		err := http.ListenAndServe(jwksAddr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			keys, err := generator.JWKS()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)

				return
			}

			_ = json.NewEncoder(w).Encode(keys)
		}))
		fmt.Fprintln(os.Stderr, "JWKS server stopped:", err)
		os.Exit(1)
	}()

	if err := waitForReady(time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	client = realtimev1connect.NewRealtimeFetchServiceClient(connectrpc.NewClient(connecthttp.NewTransport(http.DefaultClient, serverURL)))

	os.Exit(m.Run())
}

func TestRealtime(t *testing.T) {
	t.Run("push で受けた配信は宛先の呼び出し元にだけ返り、Firestore の最終更新 ID が書き換わる", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		order := newOrder(event, "user-1")

		if status := pushOrder(t, marshal(t, order)); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		if got := latestDeliveryID(t, event); got != order.GetDeliveryId() {
			t.Errorf("Firestore の最終更新 ID = %q, want %q", got, order.GetDeliveryId())
		}

		mine := fetch(t, event, "user-1", nil, 0)
		if len(mine.GetEnvelopes()) != 1 || mine.GetEnvelopes()[0].GetDeliveryId() != order.GetDeliveryId() {
			t.Errorf("宛先の envelopes = %v", mine.GetEnvelopes())
		}

		payload := new(operationv1.DeliveryPayload)
		if err := proto.Unmarshal(mine.GetEnvelopes()[0].GetPayload(), payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}

		if !proto.Equal(payload, order.GetPayload()) {
			t.Errorf("payload = %v, want %v", payload, order.GetPayload())
		}

		others := fetch(t, event, "user-2", nil, 0)
		if len(others.GetEnvelopes()) != 0 {
			t.Errorf("宛先でない呼び出し元の envelopes = %v, want none", others.GetEnvelopes())
		}

		if others.GetLatestDeliveryId() != order.GetDeliveryId() {
			t.Errorf("latest_delivery_id = %q, want %q", others.GetLatestDeliveryId(), order.GetDeliveryId())
		}
	})

	t.Run("同じ delivery_id の再配信は 1 件として残り、resume_after_delivery_id より後だけが返る", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		first := newOrder(event, "user-1")
		second := newOrder(event, "user-1")

		for _, order := range []*operationv1.DeliveryOrder{first, second, first} {
			if status := pushOrder(t, marshal(t, order)); status != http.StatusNoContent {
				t.Fatalf("push status = %d, want 204", status)
			}
		}

		all := fetch(t, event, "user-1", nil, 0)
		if len(all.GetEnvelopes()) != 2 {
			t.Fatalf("envelopes = %d, want 2", len(all.GetEnvelopes()))
		}

		resume := first.GetDeliveryId()
		after := fetch(t, event, "user-1", &resume, 0)

		if len(after.GetEnvelopes()) != 1 || after.GetEnvelopes()[0].GetDeliveryId() != second.GetDeliveryId() {
			t.Errorf("resume 後の envelopes = %v, want only %s", after.GetEnvelopes(), second.GetDeliveryId())
		}

		if got := latestDeliveryID(t, event); got != second.GetDeliveryId() {
			t.Errorf("再配信の後の最終更新 ID = %q, want %q", got, second.GetDeliveryId())
		}
	})

	t.Run("gRPC のクライアントは TLS なしの HTTP/2 で直接つなぎ、配信を取得できる", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		order := newOrder(event, "user-1")

		if status := pushOrder(t, marshal(t, order)); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)

		grpcClient := realtimev1connect.NewRealtimeFetchServiceClient(connectrpc.NewClient(connecthttp.NewTransport(
			&http.Client{Transport: &http.Transport{Protocols: protocols}},
			serverURL,
			connecthttp.WithGRPC(),
		)))

		res, err := grpcClient.FetchDeliveries(authorized(t, event, "user-1"), &realtimev1.FetchDeliveriesRequest{EventId: event})
		if err != nil {
			t.Fatalf("FetchDeliveries() over gRPC error = %v", err)
		}

		if len(res.GetEnvelopes()) != 1 || res.GetEnvelopes()[0].GetDeliveryId() != order.GetDeliveryId() {
			t.Errorf("envelopes = %v, want only %s", res.GetEnvelopes(), order.GetDeliveryId())
		}
	})

	t.Run("配信ログにない resume_after_delivery_id は out_of_range になる", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		unknown := uuid.Must(uuid.NewV7()).String()

		_, err := client.FetchDeliveries(authorized(t, event, "user-1"), &realtimev1.FetchDeliveriesRequest{
			EventId:               event,
			ResumeAfterDeliveryId: &unknown,
		})
		if got := connectrpc.CodeOf(err); got != connectrpc.CodeOutOfRange {
			t.Errorf("code = %v, want out_of_range (error %v)", got, err)
		}
	})

	t.Run("復号できない本文は 2xx で受け取り済みにして捨てる", func(t *testing.T) {
		t.Parallel()

		if status := pushOrder(t, []byte("\xff\xff\xff")); status != http.StatusNoContent {
			t.Errorf("push status = %d, want 204", status)
		}
	})

	t.Run("ロングポーリングは新着が届くと返る", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		first := newOrder(event, "user-1")

		if status := pushOrder(t, marshal(t, first)); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		second := newOrder(event, "user-1")

		go func() {
			time.Sleep(2 * time.Second)
			pushOrder(t, marshal(t, second))
		}()

		resume := first.GetDeliveryId()
		started := time.Now()
		res := fetch(t, event, "user-1", &resume, 20)

		if len(res.GetEnvelopes()) != 1 || res.GetEnvelopes()[0].GetDeliveryId() != second.GetDeliveryId() {
			t.Errorf("envelopes = %v, want the new arrival", res.GetEnvelopes())
		}

		if elapsed := time.Since(started); elapsed < time.Second || elapsed > 10*time.Second {
			t.Errorf("returned after %v, want soon after the arrival", elapsed)
		}
	})

	t.Run("IssueFirestoreToken のカスタムトークンで、そのイベントの最終更新 ID 文書だけを event_access の期限まで読める", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		order := newOrder(event, "user-1")

		if status := pushOrder(t, marshal(t, order)); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		res, err := client.IssueFirestoreToken(authorized(t, event, "user-1"), &realtimev1.IssueFirestoreTokenRequest{EventId: event})
		if err != nil {
			t.Fatalf("IssueFirestoreToken() error = %v", err)
		}

		if until := time.Until(res.GetExpiresAt().AsTime()); until < 9*time.Minute {
			t.Errorf("expires_at は %v 後、want event_access の exp（10 分後）", until)
		}

		idToken := signIn(t, res.GetFirebaseCustomToken())

		if status := readNotification(t, event, idToken); status != http.StatusOK {
			t.Errorf("自イベントの文書の読み取り = %d, want 200", status)
		}

		otherEvent := newEventID()
		if status := pushOrder(t, marshal(t, newOrder(otherEvent, "user-1"))); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		if status := readNotification(t, otherEvent, idToken); status != http.StatusForbidden {
			t.Errorf("他イベントの文書の読み取り = %d, want 403", status)
		}
	})

	t.Run("token_use が delivery_notification でないか、expires_at を過ぎたトークンでは読めない", func(t *testing.T) {
		t.Parallel()

		event := newEventID()
		if status := pushOrder(t, marshal(t, newOrder(event, "user-1"))); status != http.StatusNoContent {
			t.Fatalf("push status = %d, want 204", status)
		}

		tests := map[string]map[string]any{
			"Edge Bridge のトークン": {"event_id": event, "token_use": "signaling", "expires_at": time.Now().Add(time.Hour).Unix()},
			"期限切れのトークン":         {"event_id": event, "token_use": "delivery_notification", "expires_at": time.Now().Add(-time.Minute).Unix()},
		}

		for name, claims := range tests {
			idToken := signIn(t, adminCustomToken(t, claims))
			if status := readNotification(t, event, idToken); status != http.StatusForbidden {
				t.Errorf("%s での読み取り = %d, want 403", name, status)
			}
		}
	})
}

func newEventID() string {
	return uuid.NewString()[:8] + uuid.NewString()[:8]
}

func newOrder(event string, recipients ...string) *operationv1.DeliveryOrder {
	return &operationv1.DeliveryOrder{
		DeliveryId:       uuid.Must(uuid.NewV7()).String(),
		EventId:          event,
		RecipientUserIds: recipients,
		Payload: &operationv1.DeliveryPayload{Kind: &operationv1.DeliveryPayload_StaffMessage{
			StaffMessage: &operationv1.StaffMessage{MessageId: uuid.NewString(), Body: "e2e"},
		}},
	}
}

func marshal(t *testing.T, message proto.Message) []byte {
	t.Helper()

	body, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return body
}

func pushOrder(t *testing.T, body []byte) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, serverURL+"/push/realtime-delivery", bytes.NewReader(body))
	if err != nil {
		t.Errorf("new push request: %v", err)

		return 0
	}

	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-goog-pubsub-message-id", uuid.NewString())
	req.Header.Set("x-goog-pubsub-publish-time", time.Now().UTC().Format(time.RFC3339Nano))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("push: %v", err)

		return 0
	}
	defer res.Body.Close()

	return res.StatusCode
}

func authorized(t *testing.T, event, user string) context.Context {
	t.Helper()

	output, err := generator.Generate(jwtgen.Config{
		Issuer:         "service-gateway",
		Audience:       "tolo-realtime",
		TokenUse:       internaljwt.TokenUseEventAccess,
		TenantPublicID: "0123456789abcdef",
		EventPublicID:  event,
		Scope:          "events.read",
		Subject:        user,
		TTL:            10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("generate internal JWT: %v", err)
	}

	ctx, info := connectrpc.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+output.Token)

	return ctx
}

func fetch(t *testing.T, event, user string, resume *string, longPollSeconds uint32) *realtimev1.FetchDeliveriesResponse {
	t.Helper()

	req := &realtimev1.FetchDeliveriesRequest{EventId: event, ResumeAfterDeliveryId: resume}
	if longPollSeconds > 0 {
		req.LongPollTimeoutSeconds = &longPollSeconds
	}

	res, err := client.FetchDeliveries(authorized(t, event, user), req)
	if err != nil {
		t.Fatalf("FetchDeliveries() error = %v", err)
	}

	return res
}

func latestDeliveryID(t *testing.T, event string) string {
	t.Helper()

	var doc struct {
		Fields struct {
			LatestDeliveryID struct {
				StringValue string `json:"stringValue"`
			} `json:"latestDeliveryId"`
		} `json:"fields"`
	}

	status, body := getNotification(t, event, "owner")
	if status != http.StatusOK {
		t.Fatalf("read notification as owner = %d: %s", status, body)
	}

	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode notification: %v", err)
	}

	return doc.Fields.LatestDeliveryID.StringValue
}

func readNotification(t *testing.T, event, idToken string) int {
	t.Helper()

	status, _ := getNotification(t, event, idToken)

	return status
}

func getNotification(t *testing.T, event, bearer string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, firestoreURL+"/deliveryNotifications/"+event, nil)
	if err != nil {
		t.Fatalf("new firestore request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+bearer)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read notification body: %v", err)
	}

	return res.StatusCode, body
}

func signIn(t *testing.T, customToken string) string {
	t.Helper()

	body, err := json.Marshal(map[string]any{"token": customToken, "returnSecureToken": true})
	if err != nil {
		t.Fatalf("marshal sign-in request: %v", err)
	}

	res, err := http.Post(authURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer res.Body.Close()

	var signedIn struct {
		IDToken string `json:"idToken"`
	}

	if err := json.NewDecoder(res.Body).Decode(&signedIn); err != nil || signedIn.IDToken == "" {
		t.Fatalf("sign in with custom token: status %d, error %v", res.StatusCode, err)
	}

	return signedIn.IDToken
}

func adminCustomToken(t *testing.T, claims map[string]any) string {
	t.Helper()

	app, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: "demo-tolo"})
	if err != nil {
		t.Fatalf("create firebase app: %v", err)
	}

	auth, err := app.Auth(context.Background())
	if err != nil {
		t.Fatalf("create firebase auth client: %v", err)
	}

	token, err := auth.CustomTokenWithClaims(context.Background(), "e2e-other", claims)
	if err != nil {
		t.Fatalf("create custom token: %v", err)
	}

	return token
}

func waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		res, err := http.Get(serverURL + "/readyz")
		if err == nil {
			res.Body.Close()

			if res.StatusCode == http.StatusOK {
				return nil
			}
		}

		time.Sleep(time.Second)
	}

	return fmt.Errorf("server was not ready within %v", timeout)
}
