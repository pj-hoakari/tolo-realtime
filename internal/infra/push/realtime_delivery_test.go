package push_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	operationv1 "github.com/pj-hoakari/tolo-operation/gen/tolo/operation/v1"
	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/infra/push"
)

const deliveryID = "0192c3b4-5d6e-7f00-8a1b-2c3d4e5f6071"

type recordingReceiver struct {
	received []domain.Delivery
	err      error
}

func (r *recordingReceiver) Receive(_ context.Context, delivery domain.Delivery) error {
	r.received = append(r.received, delivery)

	return r.err
}

func order() *operationv1.DeliveryOrder {
	return &operationv1.DeliveryOrder{
		DeliveryId:       deliveryID,
		EventId:          "0123456789abcdef",
		RecipientUserIds: []string{"user-1", "user-2"},
		Payload: &operationv1.DeliveryPayload{Kind: &operationv1.DeliveryPayload_StaffMessage{
			StaffMessage: &operationv1.StaffMessage{MessageId: "message-1", Body: "hello"},
		}},
	}
}

func marshal(t *testing.T, message proto.Message) []byte {
	t.Helper()

	body, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return body
}

func post(t *testing.T, receiver *recordingReceiver, body []byte, header map[string]string) int {
	t.Helper()

	mux := http.NewServeMux()
	push.Routes(receiver)(mux)

	req := httptest.NewRequest(http.MethodPost, push.RealtimeDeliveryPath, bytes.NewReader(body))
	for key, value := range header {
		req.Header.Set(key, value)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	return recorder.Code
}

func TestRealtimeDeliveryIsLoggedWithThePublishTime(t *testing.T) {
	t.Parallel()

	receiver := &recordingReceiver{}
	code := post(t, receiver, marshal(t, order()), map[string]string{
		"x-goog-pubsub-message-id":   "message-1",
		"x-goog-pubsub-ordering-key": "0123456789abcdef",
		"x-goog-pubsub-publish-time": "2026-10-24T09:00:01.5Z",
	})

	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}

	if len(receiver.received) != 1 {
		t.Fatalf("received = %d deliveries, want 1", len(receiver.received))
	}

	got := receiver.received[0]
	if got.ID != deliveryID || got.EventID != "0123456789abcdef" || len(got.Recipients) != 2 {
		t.Errorf("delivery = %+v", got)
	}

	if want := time.Date(2026, 10, 24, 9, 0, 1, 500_000_000, time.UTC); !got.SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", got.SentAt, want)
	}

	if !proto.Equal(got.Payload, order().GetPayload()) {
		t.Errorf("payload = %v", got.Payload)
	}
}

func TestRealtimeDeliveryWithoutPublishTimeIsSentAtItsDeliveryIDTime(t *testing.T) {
	t.Parallel()

	receiver := &recordingReceiver{}
	if code := post(t, receiver, marshal(t, order()), nil); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}

	if want := time.UnixMilli(0x0192c3b45d6e).UTC(); !receiver.received[0].SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", receiver.received[0].SentAt, want)
	}
}

func TestUndecodableRealtimeDeliveryIsAcknowledgedAndDropped(t *testing.T) {
	t.Parallel()

	withoutRecipients := order()
	withoutRecipients.RecipientUserIds = nil

	notUUIDv7 := order()
	notUUIDv7.DeliveryId = "0192c3b4-5d6e-4f00-8a1b-2c3d4e5f6071"

	withoutPayload := order()
	withoutPayload.Payload = nil

	tests := map[string][]byte{
		"not a DeliveryOrder":        []byte("\xff\xff\xff"),
		"delivery ID not a UUIDv7":   marshal(t, notUUIDv7),
		"no event":                   marshal(t, &operationv1.DeliveryOrder{DeliveryId: deliveryID, RecipientUserIds: []string{"u"}, Payload: order().GetPayload()}),
		"no recipients":              marshal(t, withoutRecipients),
		"no payload":                 marshal(t, withoutPayload),
		"larger than the body limit": bytes.Repeat([]byte{0}, 1<<20+1),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			receiver := &recordingReceiver{}
			if code := post(t, receiver, body, nil); code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", code, http.StatusNoContent)
			}

			if len(receiver.received) != 0 {
				t.Errorf("received = %v, want the delivery dropped", receiver.received)
			}
		})
	}
}

func TestRealtimeDeliveryIsRetriedWhenItCannotBeStored(t *testing.T) {
	t.Parallel()

	receiver := &recordingReceiver{err: errors.New("database is down")}
	if code := post(t, receiver, marshal(t, order()), nil); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", code, http.StatusServiceUnavailable)
	}
}

func TestRealtimeDeliveryAcceptsOnlyPost(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	push.Routes(&recordingReceiver{})(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, push.RealtimeDeliveryPath, nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
