package application

import (
	"context"
	"fmt"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

type LatestDeliveryNotifier interface {
	NotifyLatest(ctx context.Context, eventID string, latestID domain.DeliveryID) error
}

type DeliveryIntakeService struct {
	log      repository.DeliveryLog
	notifier LatestDeliveryNotifier
}

func NewDeliveryIntakeService(log repository.DeliveryLog, notifier LatestDeliveryNotifier) *DeliveryIntakeService {
	return &DeliveryIntakeService{log: log, notifier: notifier}
}

func (s *DeliveryIntakeService) Receive(ctx context.Context, delivery domain.Delivery) error {
	if err := s.log.Append(ctx, delivery); err != nil {
		return err
	}

	latestID, err := s.log.LatestID(ctx, delivery.EventID)
	if err != nil {
		return err
	}

	if err := s.notifier.NotifyLatest(ctx, delivery.EventID, latestID); err != nil {
		return fmt.Errorf("notify latest delivery: %w", err)
	}

	return nil
}
