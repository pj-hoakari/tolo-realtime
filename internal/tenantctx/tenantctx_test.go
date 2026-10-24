package tenantctx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"

	"github.com/pj-hoakari/tolo-realtime/internal/tenantctx"
)

var accessExpiresAt = time.Date(2026, 10, 24, 9, 10, 0, 0, time.UTC)

func eventAccess(subject, eventID string) internaljwt.Claims {
	return internaljwt.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: subject},
		TokenUse:         internaljwt.TokenUseEventAccess,
		EventPublicID:    eventID,
		SourceExpiresAt:  jwt.NewNumericDate(accessExpiresAt),
	}
}

func TestEventCallerFor(t *testing.T) {
	t.Parallel()

	serviceClaims := eventAccess("tolo-operation", "event-1")
	serviceClaims.TokenUse = internaljwt.TokenUseService

	withoutSourceExpiry := eventAccess("user-1", "event-1")
	withoutSourceExpiry.SourceExpiresAt = nil

	tests := []struct {
		name    string
		ctx     context.Context
		want    tenantctx.EventCaller
		wantErr error
	}{
		{
			name: "event access for the requested event",
			ctx:  internaljwt.ContextWithClaims(context.Background(), eventAccess("user-1", "event-1")),
			want: tenantctx.EventCaller{UserID: "user-1", AccessExpiresAt: accessExpiresAt},
		},
		{
			name:    "no claims",
			ctx:     context.Background(),
			wantErr: tenantctx.ErrUnauthenticated,
		},
		{
			name:    "service token",
			ctx:     internaljwt.ContextWithClaims(context.Background(), serviceClaims),
			wantErr: tenantctx.ErrUnauthenticated,
		},
		{
			name:    "blank subject",
			ctx:     internaljwt.ContextWithClaims(context.Background(), eventAccess("  ", "event-1")),
			wantErr: tenantctx.ErrUnauthenticated,
		},
		{
			name:    "no source token expiry",
			ctx:     internaljwt.ContextWithClaims(context.Background(), withoutSourceExpiry),
			wantErr: tenantctx.ErrUnauthenticated,
		},
		{
			name:    "another event",
			ctx:     internaljwt.ContextWithClaims(context.Background(), eventAccess("user-1", "event-2")),
			wantErr: tenantctx.ErrEventMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tenantctx.EventCallerFor(tt.ctx, "event-1")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EventCallerFor() error = %v, want %v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Errorf("EventCallerFor() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
