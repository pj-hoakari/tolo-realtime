package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	internaljwt "github.com/pj-hoakari/internal-jwt-handling"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

const testEvent = "0123456789abcdef"

func eventAccessContext() context.Context {
	return internaljwt.ContextWithClaims(context.Background(), internaljwt.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		TokenUse:         internaljwt.TokenUseEventAccess,
		EventPublicID:    testEvent,
		SourceExpiresAt:  jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
	})
}

type memoryLog struct {
	mu         sync.Mutex
	deliveries []domain.Delivery
	appendErr  error
	lists      int
}

func (l *memoryLog) Append(_ context.Context, delivery domain.Delivery) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.appendErr != nil {
		return l.appendErr
	}

	for _, d := range l.deliveries {
		if d.ID == delivery.ID {
			return nil
		}
	}

	l.deliveries = append(l.deliveries, delivery)

	return nil
}

func (l *memoryLog) LatestID(context.Context, string) (domain.DeliveryID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.deliveries) == 0 {
		return "", nil
	}

	return l.deliveries[len(l.deliveries)-1].ID, nil
}

func (l *memoryLog) ListAfter(ctx context.Context, eventID, _ string, after domain.DeliveryID, _ int) (repository.DeliveryPage, error) {
	page, err := l.ListRecent(ctx, eventID, "", 0)
	for i, d := range page.Deliveries {
		if d.ID == after {
			page.Deliveries = page.Deliveries[i+1:]

			return page, err
		}
	}

	return repository.DeliveryPage{}, domain.ErrResumePointNotFound
}

func (l *memoryLog) ListRecent(ctx context.Context, eventID, _ string, _ int) (repository.DeliveryPage, error) {
	latest, _ := l.LatestID(ctx, eventID)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.lists++

	return repository.DeliveryPage{Deliveries: append([]domain.Delivery(nil), l.deliveries...), LatestID: latest}, nil
}

type recordingNotifier struct {
	notified []domain.DeliveryID
	err      error
}

func (n *recordingNotifier) NotifyLatest(_ context.Context, _ string, latestID domain.DeliveryID) error {
	n.notified = append(n.notified, latestID)

	return n.err
}

func delivery(id string) domain.Delivery {
	return domain.Delivery{ID: domain.DeliveryID(id), EventID: testEvent, Recipients: []string{"user-1"}}
}

const (
	firstID  = "0192c3b4-0000-7000-8000-000000000001"
	secondID = "0192c3b4-0000-7000-8000-000000000002"
)

func TestReceiveNotifiesTheLatestDeliveryEvenForARedelivery(t *testing.T) {
	t.Parallel()

	log := &memoryLog{}
	notifier := &recordingNotifier{}
	intake := NewDeliveryIntakeService(log, notifier)

	for _, id := range []string{firstID, secondID, firstID} {
		if err := intake.Receive(context.Background(), delivery(id)); err != nil {
			t.Fatalf("Receive(%s) error = %v", id, err)
		}
	}

	want := []domain.DeliveryID{firstID, secondID, secondID}
	if len(notifier.notified) != len(want) {
		t.Fatalf("notified = %v, want %v", notifier.notified, want)
	}

	for i := range want {
		if notifier.notified[i] != want[i] {
			t.Errorf("notified = %v, want %v", notifier.notified, want)
		}
	}
}

func TestReceiveFailsWithoutNotifyingWhenTheLogFails(t *testing.T) {
	t.Parallel()

	errDown := errors.New("database is down")
	notifier := &recordingNotifier{}
	intake := NewDeliveryIntakeService(&memoryLog{appendErr: errDown}, notifier)

	if err := intake.Receive(context.Background(), delivery(firstID)); !errors.Is(err, errDown) {
		t.Fatalf("Receive() error = %v, want %v", err, errDown)
	}

	if len(notifier.notified) != 0 {
		t.Errorf("notified = %v, want nothing", notifier.notified)
	}
}

func TestReceiveFailsWhenTheNotificationFails(t *testing.T) {
	t.Parallel()

	errDown := errors.New("firestore is down")
	intake := NewDeliveryIntakeService(&memoryLog{}, &recordingNotifier{err: errDown})

	if err := intake.Receive(context.Background(), delivery(firstID)); !errors.Is(err, errDown) {
		t.Fatalf("Receive() error = %v, want %v", err, errDown)
	}
}

func TestLongPollReturnsAsSoonAsADeliveryArrives(t *testing.T) {
	t.Parallel()

	log := &memoryLog{deliveries: []domain.Delivery{delivery(firstID)}}
	fetch := &DeliveryFetchService{log: log, pollInterval: 10 * time.Millisecond}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_ = log.Append(context.Background(), delivery(secondID))
	}()

	resume := firstID
	started := time.Now()

	page, err := fetch.FetchDeliveries(eventAccessContext(), FetchDeliveriesInput{
		EventID:         testEvent,
		ResumeAfter:     &resume,
		LongPollTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("FetchDeliveries() error = %v", err)
	}

	if len(page.Deliveries) != 1 || page.Deliveries[0].ID != secondID {
		t.Errorf("deliveries = %v, want the new arrival", page.Deliveries)
	}

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("returned after %v, want soon after the arrival", elapsed)
	}
}

func TestLongPollReturnsNothingWhenTheTimeoutPasses(t *testing.T) {
	t.Parallel()

	log := &memoryLog{deliveries: []domain.Delivery{delivery(firstID)}}
	fetch := &DeliveryFetchService{log: log, pollInterval: 10 * time.Millisecond}

	resume := firstID

	page, err := fetch.FetchDeliveries(eventAccessContext(), FetchDeliveriesInput{
		EventID:         testEvent,
		ResumeAfter:     &resume,
		LongPollTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("FetchDeliveries() error = %v", err)
	}

	if len(page.Deliveries) != 0 || page.LatestID != firstID {
		t.Errorf("page = %+v, want no deliveries and the resume point as latest", page)
	}

	if log.lists < 2 {
		t.Errorf("log was read %d times, want it polled during the wait", log.lists)
	}
}

func TestFetchWithoutLongPollDoesNotWait(t *testing.T) {
	t.Parallel()

	log := &memoryLog{}
	fetch := NewDeliveryFetchService(log)

	if _, err := fetch.FetchDeliveries(eventAccessContext(), FetchDeliveriesInput{EventID: testEvent}); err != nil {
		t.Fatalf("FetchDeliveries() error = %v", err)
	}

	if log.lists != 1 {
		t.Errorf("log was read %d times, want once", log.lists)
	}
}
