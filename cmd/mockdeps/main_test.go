package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pj-hoakari/internal-jwt-handling/jwks"
	"github.com/pj-hoakari/internal-jwt-handling/jwtgen"
	"github.com/pj-hoakari/internal-jwt-handling/verifier"

	connectinfra "github.com/pj-hoakari/tolo-realtime/internal/infra/connect"
)

func TestIssuedTokenVerifiesAgainstServedJWKS(t *testing.T) {
	generator, err := jwtgen.NewGenerator("")
	if err != nil {
		t.Fatal(err)
	}

	mux, err := newMux(generator)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"tenant_id":"0123456789abcdef","event_id":"fedcba9876543210","subject":"staff-user-1","ttl":"5m"}`

	res, err := http.Post(srv.URL+"/token", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /token status = %d", res.StatusCode)
	}

	var output jwtgen.Output
	if err := json.NewDecoder(res.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}

	cache, err := jwks.New(jwks.Config{URL: srv.URL + "/.well-known/jwks.json"})
	if err != nil {
		t.Fatal(err)
	}

	tokenVerifier, err := verifier.New(connectinfra.DefaultInternalJWTIssuer, connectinfra.DefaultInternalJWTAudience, cache)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := tokenVerifier.Verify(t.Context(), output.Token)
	if err != nil {
		t.Fatalf("verify issued token: %v", err)
	}

	if claims.TokenUse != "event_access" || claims.TenantPublicID != "0123456789abcdef" ||
		claims.EventPublicID != "fedcba9876543210" || claims.Subject != "staff-user-1" ||
		claims.Scope != "events.read" {
		t.Fatalf("claims = %+v", claims)
	}

	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 5*time.Minute {
		t.Fatalf("token lifetime = %v, want 5m", got)
	}
}
