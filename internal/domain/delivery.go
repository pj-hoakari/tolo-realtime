package domain

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	operationv1 "github.com/pj-hoakari/tolo-operation/gen/tolo/operation/v1"
)

var (
	ErrInvalidDeliveryID   = errors.New("delivery ID must be a UUIDv7")
	ErrEventIDRequired     = errors.New("event ID is required")
	ErrRecipientsRequired  = errors.New("delivery has no recipients")
	ErrPayloadRequired     = errors.New("delivery payload is required")
	ErrResumePointNotFound = errors.New("resume point is not in the delivery log")
)

type DeliveryID string

func ParseDeliveryID(s string) (DeliveryID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id.Version() != 7 {
		return "", fmt.Errorf("%w: %q", ErrInvalidDeliveryID, s)
	}

	return DeliveryID(id.String()), nil
}

func (id DeliveryID) issuedAt() time.Time {
	sec, nsec := uuid.MustParse(string(id)).Time().UnixTime()

	return time.Unix(sec, nsec).UTC()
}

type Delivery struct {
	ID         DeliveryID
	EventID    string
	Recipients []string
	Payload    *operationv1.DeliveryPayload
	SentAt     time.Time
}

func NewDelivery(order *operationv1.DeliveryOrder, publishedAt time.Time) (Delivery, error) {
	id, err := ParseDeliveryID(order.GetDeliveryId())
	if err != nil {
		return Delivery{}, err
	}

	if order.GetEventId() == "" {
		return Delivery{}, ErrEventIDRequired
	}

	if len(order.GetRecipientUserIds()) == 0 {
		return Delivery{}, ErrRecipientsRequired
	}

	if order.GetPayload() == nil {
		return Delivery{}, ErrPayloadRequired
	}

	sentAt := publishedAt
	if sentAt.IsZero() {
		sentAt = id.issuedAt()
	}

	return Delivery{
		ID:         id,
		EventID:    order.GetEventId(),
		Recipients: slices.Clone(order.GetRecipientUserIds()),
		Payload:    order.GetPayload(),
		SentAt:     sentAt,
	}, nil
}
