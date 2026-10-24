package repository

import (
	"context"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
)

type DeliveryPage struct {
	Deliveries []domain.Delivery
	LatestID   domain.DeliveryID
}

type DeliveryLog interface {
	Append(ctx context.Context, delivery domain.Delivery) error
	LatestID(ctx context.Context, eventID string) (domain.DeliveryID, error)
	ListAfter(ctx context.Context, eventID, recipient string, after domain.DeliveryID, limit int) (DeliveryPage, error)
	ListRecent(ctx context.Context, eventID, recipient string, limit int) (DeliveryPage, error)
}
