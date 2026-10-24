package firebaseauth

import (
	"context"
	"fmt"
	"time"

	"firebase.google.com/go/v4/auth"
)

const (
	claimEventID   = "event_id"
	claimExpiresAt = "expires_at"
	claimTokenUse  = "token_use"

	tokenUseDeliveryNotification = "delivery_notification"
)

type TokenMinter struct {
	client *auth.Client
}

func NewTokenMinter(client *auth.Client) *TokenMinter {
	return &TokenMinter{client: client}
}

func (m *TokenMinter) Mint(ctx context.Context, userID, eventID string, expiresAt time.Time) (string, error) {
	token, err := m.client.CustomTokenWithClaims(ctx, userID, map[string]any{
		claimEventID:   eventID,
		claimExpiresAt: expiresAt.Unix(),
		claimTokenUse:  tokenUseDeliveryNotification,
	})
	if err != nil {
		return "", fmt.Errorf("create custom token: %w", err)
	}

	return token, nil
}
