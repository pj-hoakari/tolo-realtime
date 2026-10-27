package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"
	"github.com/pj-hoakari/internal-jwt-handling/jwks"
	"github.com/pj-hoakari/internal-jwt-handling/jwtgen"
	"github.com/pj-hoakari/internal-jwt-handling/verifier"

	operationv1 "github.com/pj-hoakari/tolo-operation/gen/tolo/operation/v1"
	realtimev1 "github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1"
	"github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1/realtimev1connect"
	"github.com/pj-hoakari/tolo-realtime/internal/application"
	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

// newTestJWKSURL serves keys from an httptest endpoint, mirroring the Service
// Gateway publishing its signing keys, and returns the URL to fetch them from.
func newTestJWKSURL(t *testing.T, keys internaljwt.JWKS) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(keys); err != nil {
			t.Errorf("encode JWKS: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL
}

// newTestVerifier builds a verifier backed by an httptest JWKS endpoint serving
// keys.
func newTestVerifier(t *testing.T, keys internaljwt.JWKS) *verifier.Verifier {
	t.Helper()

	// The cooldowns are collapsed so that a refresh on an unknown kid is never
	// held off within a test run.
	cache, err := jwks.New(jwks.Config{
		URL:             newTestJWKSURL(t, keys),
		RefreshCooldown: time.Nanosecond,
		FailureCooldown: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("create JWKS cache: %v", err)
	}

	tokenVerifier, err := verifier.New(DefaultInternalJWTIssuer, DefaultInternalJWTAudience, cache)
	if err != nil {
		t.Fatalf("create internal JWT verifier: %v", err)
	}

	return tokenVerifier
}

// newTestHandler builds a handler serving the production service routes wired
// to a verifier trusting keys, serving greetService.
func newTestHandler(t *testing.T, keys internaljwt.JWKS, fetch DeliveryFetchUseCase, tokens FirestoreTokenUseCase) http.Handler {
	t.Helper()

	routes, err := RoutesWithVerifier(fetch, tokens, newTestVerifier(t, keys))
	if err != nil {
		t.Fatalf("RoutesWithVerifier() error = %v", err)
	}

	mux := http.NewServeMux()
	routes(mux)

	return mux
}

func newTestHandlerForJWKSURL(t *testing.T, jwksURL string, fetch DeliveryFetchUseCase, tokens FirestoreTokenUseCase) http.Handler {
	t.Helper()

	cache, err := jwks.New(jwks.Config{
		URL:             jwksURL,
		RefreshCooldown: time.Nanosecond,
		FailureCooldown: time.Nanosecond,
		RetryBackoff:    []time.Duration{},
	})
	if err != nil {
		t.Fatalf("create JWKS cache: %v", err)
	}

	tokenVerifier, err := verifier.New(DefaultInternalJWTIssuer, DefaultInternalJWTAudience, cache)
	if err != nil {
		t.Fatalf("create internal JWT verifier: %v", err)
	}

	routes, err := RoutesWithVerifier(fetch, tokens, tokenVerifier)
	if err != nil {
		t.Fatalf("RoutesWithVerifier() error = %v", err)
	}

	mux := http.NewServeMux()
	routes(mux)

	return mux
}

// mintInternalJWT issues an internal JWT for the issuer and audience this
// service verifies against.
const (
	testEvent = "0123456789abcdef"
	testUser  = "user-1"
)

func mintEventAccess(t *testing.T, audience, tokenUse, scope string) (string, internaljwt.JWKS) {
	t.Helper()

	return mintEventAccessWithKey(t, "test-key", audience, tokenUse, scope)
}

func mintEventAccessWithKey(t *testing.T, keyID, audience, tokenUse, scope string) (string, internaljwt.JWKS) {
	t.Helper()

	output, err := jwtgen.Generate(jwtgen.Config{
		Issuer:         DefaultInternalJWTIssuer,
		Audience:       audience,
		TokenUse:       tokenUse,
		TenantPublicID: "a1b2c3d4e5f60718",
		EventPublicID:  testEvent,
		Scope:          scope,
		Subject:        testUser,
		KeyID:          keyID,
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("generate internal JWT: %v", err)
	}

	return "Bearer " + output.Token, output.JWKS
}

type stubDeliveryLog struct {
	page      repository.DeliveryPage
	err       error
	recipient string
}

func (l *stubDeliveryLog) Append(context.Context, domain.Delivery) error { return nil }

func (l *stubDeliveryLog) LatestID(context.Context, string) (domain.DeliveryID, error) {
	return l.page.LatestID, nil
}

func (l *stubDeliveryLog) ListAfter(
	_ context.Context,
	_, recipient string,
	_ domain.DeliveryID,
	_ int,
) (repository.DeliveryPage, error) {
	l.recipient = recipient

	return l.page, l.err
}

func (l *stubDeliveryLog) ListRecent(_ context.Context, _, recipient string, _ int) (repository.DeliveryPage, error) {
	l.recipient = recipient

	return l.page, l.err
}

type recordingMinter struct {
	userID    string
	eventID   string
	expiresAt time.Time
}

func (m *recordingMinter) Mint(_ context.Context, userID, eventID string, expiresAt time.Time) (string, error) {
	m.userID, m.eventID, m.expiresAt = userID, eventID, expiresAt

	return "custom-token", nil
}

type testServer struct {
	client realtimev1connect.RealtimeFetchServiceClient
	log    *stubDeliveryLog
	minter *recordingMinter
}

func newTestServer(t *testing.T, keys internaljwt.JWKS) testServer {
	t.Helper()

	log := &stubDeliveryLog{}
	minter := &recordingMinter{}
	handler := newTestHandler(t, keys,
		application.NewDeliveryFetchService(log),
		application.NewFirestoreTokenService(minter))

	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	client := realtimev1connect.NewRealtimeFetchServiceClient(
		connectrpc.NewClient(connecthttp.NewTransport(httpServer.Client(), httpServer.URL)))

	return testServer{client: client, log: log, minter: minter}
}

func withAuthorization(authorization string) context.Context {
	ctx, info := connectrpc.NewClientContext(context.Background())
	if authorization != "" {
		info.RequestHeader().Set("Authorization", authorization)
	}

	return ctx
}

func TestFetchDeliveriesReturnsTheCallersEnvelopes(t *testing.T) {
	t.Parallel()

	authorization, keys := mintEventAccess(t, DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "events.read")
	server := newTestServer(t, keys)

	sentAt := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

	payload, err := proto.Marshal(&operationv1.DeliveryPayload{Kind: &operationv1.DeliveryPayload_StaffMessage{
		StaffMessage: &operationv1.StaffMessage{MessageId: "message-1", Body: "hello"},
	}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	server.log.page = repository.DeliveryPage{
		Deliveries: []domain.Delivery{{ID: "0192c3b4-0000-7000-8000-000000000001", EventID: testEvent, Payload: payload, SentAt: sentAt}},
		LatestID:   "0192c3b4-0000-7000-8000-000000000002",
	}

	res, err := server.client.FetchDeliveries(withAuthorization(authorization), &realtimev1.FetchDeliveriesRequest{EventId: testEvent})
	if err != nil {
		t.Fatalf("FetchDeliveries() error = %v", err)
	}

	if server.log.recipient != testUser {
		t.Errorf("deliveries were read for %q, want the caller %q", server.log.recipient, testUser)
	}

	if got := res.GetLatestDeliveryId(); got != "0192c3b4-0000-7000-8000-000000000002" {
		t.Errorf("latest_delivery_id = %q", got)
	}

	if len(res.GetEnvelopes()) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(res.GetEnvelopes()))
	}

	envelope := res.GetEnvelopes()[0]

	got := new(operationv1.DeliveryPayload)
	if err := proto.Unmarshal(envelope.GetPayload(), got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if envelope.GetDeliveryId() != "0192c3b4-0000-7000-8000-000000000001" ||
		!envelope.GetSentAt().AsTime().Equal(sentAt) ||
		got.GetStaffMessage().GetBody() != "hello" {
		t.Errorf("envelope = %v, payload = %v", envelope, got)
	}
}

func TestFetchDeliveriesAnswersErrors(t *testing.T) {
	t.Parallel()

	authorization, keys := mintEventAccess(t, DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "events.read")

	tests := map[string]struct {
		eventID string
		resume  string
		logErr  error
		want    connectrpc.Code
	}{
		"another event":           {eventID: "fedcba9876543210", want: connectrpc.CodePermissionDenied},
		"malformed resume point":  {eventID: testEvent, resume: "not-a-uuid", want: connectrpc.CodeInvalidArgument},
		"resume point not logged": {eventID: testEvent, resume: "0192c3b4-0000-7000-8000-000000000001", logErr: domain.ErrResumePointNotFound, want: connectrpc.CodeOutOfRange},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := newTestServer(t, keys)
			server.log.err = tt.logErr

			req := &realtimev1.FetchDeliveriesRequest{EventId: tt.eventID}
			if tt.resume != "" {
				req.ResumeAfterDeliveryId = &tt.resume
			}

			_, err := server.client.FetchDeliveries(withAuthorization(authorization), req)
			if got := connectrpc.CodeOf(err); got != tt.want {
				t.Fatalf("FetchDeliveries() code = %v, want %v (error %v)", got, tt.want, err)
			}
		})
	}
}

func TestRealtimeFetchServiceAuthz(t *testing.T) {
	t.Parallel()

	eventRead, keys := mintEventAccess(t, DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "events.read")
	tenantRead, tenantReadKeys := mintEventAccessWithKey(t, "tenant-read-key", DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "tenant.read")
	otherAudience, otherAudienceKeys := mintEventAccessWithKey(t, "other-audience-key", "other-service", internaljwt.TokenUseEventAccess, "events.read")

	service, err := jwtgen.Generate(jwtgen.Config{
		Issuer:   DefaultInternalJWTIssuer,
		Audience: DefaultInternalJWTAudience,
		TokenUse: internaljwt.TokenUseService,
		KeyID:    "service-key",
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("generate service JWT: %v", err)
	}

	keys.Keys = append(keys.Keys, tenantReadKeys.Keys[0], otherAudienceKeys.Keys[0], service.JWKS.Keys[0])

	server := newTestServer(t, keys)

	tests := map[string]struct {
		authorization string
		want          connectrpc.Code
	}{
		"event access with events.read": {authorization: eventRead, want: 0},
		"missing bearer token":          {authorization: "", want: connectrpc.CodeUnauthenticated},
		"missing scope":                 {authorization: tenantRead, want: connectrpc.CodePermissionDenied},
		"service token":                 {authorization: "Bearer " + service.Token, want: connectrpc.CodeUnauthenticated},
		"another audience":              {authorization: otherAudience, want: connectrpc.CodeUnauthenticated},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, fetchErr := server.client.FetchDeliveries(withAuthorization(tt.authorization), &realtimev1.FetchDeliveriesRequest{EventId: testEvent})
			if got := connectrpc.CodeOf(fetchErr); fetchErr != nil && got != tt.want || fetchErr == nil && tt.want != 0 {
				t.Errorf("FetchDeliveries() error = %v, want code %v", fetchErr, tt.want)
			}

			_, tokenErr := server.client.IssueFirestoreToken(withAuthorization(tt.authorization), &realtimev1.IssueFirestoreTokenRequest{EventId: testEvent})
			if got := connectrpc.CodeOf(tokenErr); tokenErr != nil && got != tt.want || tokenErr == nil && tt.want != 0 {
				t.Errorf("IssueFirestoreToken() error = %v, want code %v", tokenErr, tt.want)
			}
		})
	}
}

func TestIssueFirestoreTokenExpiresWithTheEventAccess(t *testing.T) {
	t.Parallel()

	authorization, keys := mintEventAccess(t, DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "events.read")
	server := newTestServer(t, keys)

	res, err := server.client.IssueFirestoreToken(withAuthorization(authorization), &realtimev1.IssueFirestoreTokenRequest{EventId: testEvent})
	if err != nil {
		t.Fatalf("IssueFirestoreToken() error = %v", err)
	}

	if res.GetFirebaseCustomToken() != "custom-token" {
		t.Errorf("firebase_custom_token = %q", res.GetFirebaseCustomToken())
	}

	if server.minter.userID != testUser || server.minter.eventID != testEvent {
		t.Errorf("minted for user %q event %q, want %q %q", server.minter.userID, server.minter.eventID, testUser, testEvent)
	}

	expiresAt := res.GetExpiresAt().AsTime()
	if !expiresAt.Equal(server.minter.expiresAt) {
		t.Errorf("expires_at = %v, want the claim's %v", expiresAt, server.minter.expiresAt)
	}

	if until := time.Until(expiresAt); until < 50*time.Minute {
		t.Errorf("expires_at is %v away, want the hour-long event access exp rather than the internal JWT's", until)
	}
}

func TestRoutesWithJWTSettingsRejectsSettingsWithoutAJWKSURL(t *testing.T) {
	t.Parallel()

	settings := DefaultJWTSettings()
	settings.JWKSURL = ""

	_, err := RoutesWithJWTSettings(application.NewDeliveryFetchService(&stubDeliveryLog{}), application.NewFirestoreTokenService(&recordingMinter{}), settings)
	if !errors.Is(err, jwks.ErrMissingURL) {
		t.Fatalf("RoutesWithJWTSettings() error = %v, want %v", err, jwks.ErrMissingURL)
	}
}

func TestRealtimeFetchServiceUnavailableWhenJWKSUnreachable(t *testing.T) {
	t.Parallel()

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(jwksServer.Close)

	authorization, _ := mintEventAccess(t, DefaultInternalJWTAudience, internaljwt.TokenUseEventAccess, "events.read")
	httpServer := httptest.NewServer(newTestHandlerForJWKSURL(t, jwksServer.URL,
		application.NewDeliveryFetchService(&stubDeliveryLog{}), application.NewFirestoreTokenService(&recordingMinter{})))
	t.Cleanup(httpServer.Close)

	client := realtimev1connect.NewRealtimeFetchServiceClient(connectrpc.NewClient(connecthttp.NewTransport(httpServer.Client(), httpServer.URL)))

	_, err := client.FetchDeliveries(withAuthorization(authorization), &realtimev1.FetchDeliveriesRequest{EventId: testEvent})
	if got, want := connectrpc.CodeOf(err), connectrpc.CodeUnavailable; got != want {
		t.Fatalf("FetchDeliveries() error code = %v, want %v", got, want)
	}
}
