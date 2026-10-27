package db

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

const testEvent = "0123456789abcdef"

func newDelivery(t *testing.T, eventID string, recipients ...string) domain.Delivery {
	t.Helper()

	return domain.Delivery{
		ID:         domain.DeliveryID(uuid.Must(uuid.NewV7()).String()),
		EventID:    eventID,
		Recipients: recipients,
		Payload:    []byte(uuid.NewString()),
		SentAt:     time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
	}
}

func newTestDeliveryLog(t *testing.T) *PostgresDeliveryLog {
	t.Helper()

	if _, err := testDB.Exec(`TRUNCATE deliveries`); err != nil {
		t.Fatalf("truncate deliveries: %v", err)
	}

	return NewPostgresDeliveryLog(testDB)
}

func appendAll(t *testing.T, log *PostgresDeliveryLog, deliveries ...domain.Delivery) {
	t.Helper()

	for _, delivery := range deliveries {
		if err := log.Append(context.Background(), delivery); err != nil {
			t.Fatalf("Append(%s) error = %v", delivery.ID, err)
		}
	}
}

func ids(deliveries []domain.Delivery) []domain.DeliveryID {
	out := make([]domain.DeliveryID, 0, len(deliveries))
	for _, delivery := range deliveries {
		out = append(out, delivery.ID)
	}

	return out
}

func assertPage(t *testing.T, got repository.DeliveryPage, wantIDs []domain.DeliveryID, wantLatest domain.DeliveryID) {
	t.Helper()

	if gotIDs := ids(got.Deliveries); !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("delivery IDs = %v, want %v", gotIDs, wantIDs)
	}

	if got.LatestID != wantLatest {
		t.Errorf("LatestID = %q, want %q", got.LatestID, wantLatest)
	}
}

func TestDeliveryLogRoundTripsTheDelivery(t *testing.T) {
	log := newTestDeliveryLog(t)
	delivery := newDelivery(t, testEvent, "user-1")
	appendAll(t, log, delivery)

	page, err := log.ListRecent(context.Background(), testEvent, "user-1", 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	if len(page.Deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(page.Deliveries))
	}

	got := page.Deliveries[0]
	if got.ID != delivery.ID || got.EventID != testEvent || !got.SentAt.Equal(delivery.SentAt) {
		t.Errorf("delivery = %+v, want %+v", got, delivery)
	}

	if !bytes.Equal(got.Payload, delivery.Payload) {
		t.Errorf("payload = %q, want %q", got.Payload, delivery.Payload)
	}
}

func TestDeliveryLogIgnoresARedeliveredDelivery(t *testing.T) {
	log := newTestDeliveryLog(t)
	first := newDelivery(t, testEvent, "user-1")
	second := newDelivery(t, testEvent, "user-1")
	appendAll(t, log, first, second, first)

	page, err := log.ListRecent(context.Background(), testEvent, "user-1", 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{first.ID, second.ID}, second.ID)
}

func TestDeliveryLogOrdersByArrivalNotByDeliveryID(t *testing.T) {
	log := newTestDeliveryLog(t)
	older := newDelivery(t, testEvent, "user-1")
	newer := newDelivery(t, testEvent, "user-1")
	appendAll(t, log, newer, older)

	latest, err := log.LatestID(context.Background(), testEvent)
	if err != nil {
		t.Fatalf("LatestID() error = %v", err)
	}

	if latest != older.ID {
		t.Errorf("LatestID() = %q, want the last arrival %q", latest, older.ID)
	}

	page, err := log.ListAfter(context.Background(), testEvent, "user-1", newer.ID, 10)
	if err != nil {
		t.Fatalf("ListAfter() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{older.ID}, older.ID)
}

func TestDeliveryLogReturnsOnlyTheCallersDeliveriesOfTheEvent(t *testing.T) {
	log := newTestDeliveryLog(t)
	mine := newDelivery(t, testEvent, "user-1", "user-2")
	others := newDelivery(t, testEvent, "user-2")
	otherEvent := newDelivery(t, "fedcba9876543210", "user-1")
	appendAll(t, log, mine, others, otherEvent)

	page, err := log.ListRecent(context.Background(), testEvent, "user-1", 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{mine.ID}, others.ID)
}

func TestDeliveryLogListAfterPagesUpToTheLimit(t *testing.T) {
	log := newTestDeliveryLog(t)
	start := newDelivery(t, testEvent, "user-1")
	a := newDelivery(t, testEvent, "user-1")
	b := newDelivery(t, testEvent, "user-1")
	c := newDelivery(t, testEvent, "user-1")
	appendAll(t, log, start, a, b, c)

	page, err := log.ListAfter(context.Background(), testEvent, "user-1", start.ID, 2)
	if err != nil {
		t.Fatalf("ListAfter() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{a.ID, b.ID}, b.ID)

	page, err = log.ListAfter(context.Background(), testEvent, "user-1", page.LatestID, 2)
	if err != nil {
		t.Fatalf("ListAfter() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{c.ID}, c.ID)
}

func TestDeliveryLogListRecentReturnsTheNewestInArrivalOrder(t *testing.T) {
	log := newTestDeliveryLog(t)
	a := newDelivery(t, testEvent, "user-1")
	b := newDelivery(t, testEvent, "user-1")
	c := newDelivery(t, testEvent, "user-1")
	appendAll(t, log, a, b, c)

	page, err := log.ListRecent(context.Background(), testEvent, "user-1", 2)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	assertPage(t, page, []domain.DeliveryID{b.ID, c.ID}, c.ID)
}

func TestDeliveryLogListAfterRejectsAnUnknownResumePoint(t *testing.T) {
	log := newTestDeliveryLog(t)
	elsewhere := newDelivery(t, "fedcba9876543210", "user-1")
	appendAll(t, log, elsewhere)

	_, err := log.ListAfter(context.Background(), testEvent, "user-1", elsewhere.ID, 10)
	if !errors.Is(err, domain.ErrResumePointNotFound) {
		t.Fatalf("ListAfter() error = %v, want %v", err, domain.ErrResumePointNotFound)
	}
}

func TestDeliveryLogOfAnEmptyEventHasNoLatestID(t *testing.T) {
	log := newTestDeliveryLog(t)

	page, err := log.ListRecent(context.Background(), testEvent, "user-1", 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}

	assertPage(t, page, nil, "")
}

func TestDeliveryLogAppendJoinsTransaction(t *testing.T) {
	log := newTestDeliveryLog(t)
	delivery := newDelivery(t, testEvent, "user-1")
	errAbort := errors.New("abort")

	err := RunInTransaction(context.Background(), testDB, func(ctx context.Context) error {
		if err := log.Append(ctx, delivery); err != nil {
			return err
		}

		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("RunInTransaction() error = %v, want %v", err, errAbort)
	}

	latest, err := log.LatestID(context.Background(), testEvent)
	if err != nil {
		t.Fatalf("LatestID() error = %v", err)
	}

	if latest != "" {
		t.Errorf("LatestID() after rollback = %q, want none", latest)
	}
}

func TestDeliveryLogAppendNormalizesQueryText(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previousProvider := otel.GetTracerProvider()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	log := newTestDeliveryLog(t)
	appendAll(t, log, newDelivery(t, testEvent, "user-1"))

	const wantQueryText = "INSERT INTO deliveries (delivery_id, event_id, recipient_user_ids, payload, sent_at) " +
		"VALUES ($1, $2, $3, $4, $5) ON CONFLICT (delivery_id) DO NOTHING"

	for _, span := range recorder.Ended() {
		if queryText, ok := spanAttribute(span, semconv.DBQueryTextKey); ok && queryText == wantQueryText {
			return
		}
	}

	t.Errorf("no span with %s = %q", semconv.DBQueryTextKey, wantQueryText)
}
