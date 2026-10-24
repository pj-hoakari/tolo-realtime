package push

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/protobuf/proto"

	operationv1 "github.com/pj-hoakari/tolo-operation/gen/tolo/operation/v1"
	"github.com/pj-hoakari/tolo-realtime/internal/domain"
)

const (
	RealtimeDeliveryPath = "/push/realtime-delivery"

	headerMessageID   = "x-goog-pubsub-message-id"
	headerOrderingKey = "x-goog-pubsub-ordering-key"
	headerPublishTime = "x-goog-pubsub-publish-time"

	maxBodyBytes = 1 << 20
)

type DeliveryReceiver interface {
	Receive(ctx context.Context, delivery domain.Delivery) error
}

func Routes(receiver DeliveryReceiver) func(mux *http.ServeMux) {
	handler := otelhttp.NewHandler(realtimeDeliveryHandler(receiver), "POST "+RealtimeDeliveryPath)

	return func(mux *http.ServeMux) {
		mux.Handle("POST "+RealtimeDeliveryPath, handler)
	}
}

func realtimeDeliveryHandler(receiver DeliveryReceiver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := slog.With(
			"message_id", r.Header.Get(headerMessageID),
			"ordering_key", r.Header.Get(headerOrderingKey),
		)

		delivery, err := decode(r)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if !errors.As(err, &tooLarge) && !errors.Is(err, errUndecodable) {
				logger.WarnContext(ctx, "read realtime delivery failed", "error", err)
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}

			logger.ErrorContext(ctx, "realtime delivery dropped as undecodable", "error", err)
			w.WriteHeader(http.StatusNoContent)

			return
		}

		if err := receiver.Receive(ctx, delivery); err != nil {
			logger.ErrorContext(ctx, "receive realtime delivery failed",
				"delivery_id", delivery.ID, "event_id", delivery.EventID, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

var errUndecodable = errors.New("undecodable realtime delivery")

func decode(r *http.Request) (domain.Delivery, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err != nil {
		return domain.Delivery{}, err
	}

	order := new(operationv1.DeliveryOrder)
	if err := proto.Unmarshal(body, order); err != nil {
		return domain.Delivery{}, errors.Join(errUndecodable, err)
	}

	publishedAt, err := time.Parse(time.RFC3339Nano, r.Header.Get(headerPublishTime))
	if err != nil {
		publishedAt = time.Time{}
	}

	delivery, err := domain.NewDelivery(order, publishedAt)
	if err != nil {
		return domain.Delivery{}, errors.Join(errUndecodable, err)
	}

	return delivery, nil
}
