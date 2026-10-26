package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"
	"github.com/pj-hoakari/internal-jwt-handling/jwtgen"

	connectinfra "github.com/pj-hoakari/tolo-realtime/internal/infra/connect"
)

func main() {
	generator, err := jwtgen.NewGenerator("")
	if err != nil {
		slog.Error("create signing key failed", "error", err)
		os.Exit(1)
	}

	mux, err := newMux(generator)
	if err != nil {
		slog.Error("build handler failed", "error", err)
		os.Exit(1)
	}

	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	slog.Info("mockdeps listening", "addr", server.Addr)

	if err := server.ListenAndServe(); err != nil {
		slog.Error("mockdeps failed", "error", err)
		os.Exit(1)
	}
}

func newMux(generator *jwtgen.Generator) (*http.ServeMux, error) {
	jwks, err := generator.JWKS()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jwks)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		issueToken(w, r, generator)
	})

	return mux, nil
}

type tokenRequest struct {
	Audience string `json:"audience"`
	TokenUse string `json:"token_use"`
	TenantID string `json:"tenant_id"`
	EventID  string `json:"event_id"`
	Subject  string `json:"subject"`
	Scope    string `json:"scope"`
	TTL      string `json:"ttl"`
}

func issueToken(w http.ResponseWriter, r *http.Request, generator *jwtgen.Generator) {
	req := tokenRequest{
		Audience: connectinfra.DefaultInternalJWTAudience,
		TokenUse: internaljwt.TokenUseEventAccess,
		TenantID: "",
		EventID:  "",
		Subject:  "",
		Scope:    "events.read",
		TTL:      "10m",
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	output, err := generator.Generate(jwtgen.Config{
		Issuer:         connectinfra.DefaultInternalJWTIssuer,
		Audience:       req.Audience,
		TokenUse:       req.TokenUse,
		TenantPublicID: req.TenantID,
		EventPublicID:  req.EventID,
		Scope:          req.Scope,
		OriginSub:      "",
		Txn:            "",
		Subject:        req.Subject,
		KeyID:          "",
		TTL:            ttl,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	writeJSON(w, output)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response failed", "error", err)
	}
}
