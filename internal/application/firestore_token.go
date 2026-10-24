package application

import (
	"context"
	"fmt"
	"time"

	"github.com/pj-hoakari/tolo-realtime/internal/tenantctx"
)

type FirestoreTokenMinter interface {
	Mint(ctx context.Context, userID, eventID string, expiresAt time.Time) (string, error)
}

type FirestoreToken struct {
	CustomToken string
	ExpiresAt   time.Time
}

type FirestoreTokenService struct {
	minter FirestoreTokenMinter
}

func NewFirestoreTokenService(minter FirestoreTokenMinter) *FirestoreTokenService {
	return &FirestoreTokenService{minter: minter}
}

func (s *FirestoreTokenService) IssueFirestoreToken(ctx context.Context, eventID string) (FirestoreToken, error) {
	caller, err := tenantctx.EventCallerFor(ctx, eventID)
	if err != nil {
		return FirestoreToken{}, err
	}

	token, err := s.minter.Mint(ctx, caller.UserID, eventID, caller.AccessExpiresAt)
	if err != nil {
		return FirestoreToken{}, fmt.Errorf("mint firestore custom token: %w", err)
	}

	return FirestoreToken{CustomToken: token, ExpiresAt: caller.AccessExpiresAt}, nil
}
