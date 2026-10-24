package tenantctx

import (
	"context"
	"errors"
	"strings"
	"time"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"
)

var (
	ErrUnauthenticated = errors.New("caller is not authenticated by an event access token")
	ErrEventMismatch   = errors.New("event ID does not match context")
)

type EventCaller struct {
	UserID          string
	AccessExpiresAt time.Time
}

func EventCallerFor(ctx context.Context, eventID string) (EventCaller, error) {
	claims, ok := internaljwt.ClaimsFromContext(ctx)
	if !ok || claims.TokenUse != internaljwt.TokenUseEventAccess {
		return EventCaller{}, ErrUnauthenticated
	}

	userID := strings.TrimSpace(claims.Subject)
	if userID == "" || claims.EventPublicID == "" || claims.SourceExpiresAt == nil {
		return EventCaller{}, ErrUnauthenticated
	}

	if claims.EventPublicID != eventID {
		return EventCaller{}, ErrEventMismatch
	}

	return EventCaller{UserID: userID, AccessExpiresAt: claims.SourceExpiresAt.UTC()}, nil
}
