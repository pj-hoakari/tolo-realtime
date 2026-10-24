package connect

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	realtimev1 "github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1"
	"github.com/pj-hoakari/tolo-realtime/gen/tolo/realtime/v1/realtimev1connect"
	"github.com/pj-hoakari/tolo-realtime/internal/application"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

type DeliveryFetchUseCase interface {
	FetchDeliveries(ctx context.Context, input application.FetchDeliveriesInput) (repository.DeliveryPage, error)
}

type FirestoreTokenUseCase interface {
	IssueFirestoreToken(ctx context.Context, eventID string) (application.FirestoreToken, error)
}

type RealtimeFetchService struct {
	realtimev1connect.UnimplementedRealtimeFetchServiceHandler

	fetch  DeliveryFetchUseCase
	tokens FirestoreTokenUseCase
}

func NewRealtimeFetchService(fetch DeliveryFetchUseCase, tokens FirestoreTokenUseCase) *RealtimeFetchService {
	return &RealtimeFetchService{
		UnimplementedRealtimeFetchServiceHandler: realtimev1connect.UnimplementedRealtimeFetchServiceHandler{},
		fetch:                                    fetch,
		tokens:                                   tokens,
	}
}

func (s *RealtimeFetchService) IssueFirestoreToken(
	ctx context.Context,
	req *realtimev1.IssueFirestoreTokenRequest,
) (*realtimev1.IssueFirestoreTokenResponse, error) {
	token, err := s.tokens.IssueFirestoreToken(ctx, req.GetEventId())
	if err != nil {
		return nil, connectError(ctx, err)
	}

	return &realtimev1.IssueFirestoreTokenResponse{
		FirebaseCustomToken: token.CustomToken,
		ExpiresAt:           timestamppb.New(token.ExpiresAt),
	}, nil
}

func (s *RealtimeFetchService) FetchDeliveries(
	ctx context.Context,
	req *realtimev1.FetchDeliveriesRequest,
) (*realtimev1.FetchDeliveriesResponse, error) {
	page, err := s.fetch.FetchDeliveries(ctx, application.FetchDeliveriesInput{
		EventID:         req.GetEventId(),
		ResumeAfter:     req.ResumeAfterDeliveryId,
		LongPollTimeout: time.Duration(req.GetLongPollTimeoutSeconds()) * time.Second,
	})
	if err != nil {
		return nil, connectError(ctx, err)
	}

	envelopes := make([]*realtimev1.Envelope, 0, len(page.Deliveries))
	for _, delivery := range page.Deliveries {
		envelopes = append(envelopes, &realtimev1.Envelope{
			DeliveryId: string(delivery.ID),
			SentAt:     timestamppb.New(delivery.SentAt),
			Payload:    delivery.Payload,
		})
	}

	return &realtimev1.FetchDeliveriesResponse{
		Envelopes:        envelopes,
		LatestDeliveryId: string(page.LatestID),
	}, nil
}
