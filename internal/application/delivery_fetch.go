package application

import (
	"context"
	"time"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
	"github.com/pj-hoakari/tolo-realtime/internal/tenantctx"
)

const (
	FetchPageSize      = 100
	MaxLongPollTimeout = 30 * time.Second
	longPollInterval   = time.Second
)

type FetchDeliveriesInput struct {
	EventID         string
	ResumeAfter     *string
	LongPollTimeout time.Duration
}

type DeliveryFetchService struct {
	log          repository.DeliveryLog
	pollInterval time.Duration
}

func NewDeliveryFetchService(log repository.DeliveryLog) *DeliveryFetchService {
	return &DeliveryFetchService{log: log, pollInterval: longPollInterval}
}

func (s *DeliveryFetchService) FetchDeliveries(
	ctx context.Context,
	input FetchDeliveriesInput,
) (repository.DeliveryPage, error) {
	caller, err := tenantctx.EventCallerFor(ctx, input.EventID)
	if err != nil {
		return repository.DeliveryPage{}, err
	}

	fetch := func() (repository.DeliveryPage, error) {
		return s.log.ListRecent(ctx, input.EventID, caller.UserID, FetchPageSize)
	}

	if input.ResumeAfter != nil {
		after, err := domain.ParseDeliveryID(*input.ResumeAfter)
		if err != nil {
			return repository.DeliveryPage{}, err
		}

		fetch = func() (repository.DeliveryPage, error) {
			return s.log.ListAfter(ctx, input.EventID, caller.UserID, after, FetchPageSize)
		}
	}

	page, err := fetch()
	if err != nil || len(page.Deliveries) > 0 || input.LongPollTimeout <= 0 {
		return page, err
	}

	deadline := time.NewTimer(min(input.LongPollTimeout, MaxLongPollTimeout))
	defer deadline.Stop()

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return repository.DeliveryPage{}, ctx.Err()
		case <-deadline.C:
			return page, nil
		case <-ticker.C:
			page, err = fetch()
			if err != nil || len(page.Deliveries) > 0 {
				return page, err
			}
		}
	}
}
