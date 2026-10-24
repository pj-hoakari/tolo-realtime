package firestore

import (
	"context"
	"fmt"

	gcpfirestore "cloud.google.com/go/firestore"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
)

const deliveryNotificationsCollection = "deliveryNotifications"

type deliveryNotificationDoc struct {
	LatestDeliveryID string `firestore:"latestDeliveryId"`
}

type LatestDeliveryNotifier struct {
	client *gcpfirestore.Client
}

func NewLatestDeliveryNotifier(client *gcpfirestore.Client) *LatestDeliveryNotifier {
	return &LatestDeliveryNotifier{client: client}
}

func (n *LatestDeliveryNotifier) NotifyLatest(ctx context.Context, eventID string, latestID domain.DeliveryID) error {
	doc := n.client.Collection(deliveryNotificationsCollection).Doc(eventID)
	if _, err := doc.Set(ctx, deliveryNotificationDoc{LatestDeliveryID: string(latestID)}); err != nil {
		return fmt.Errorf("set latest delivery of event %s: %w", eventID, err)
	}

	return nil
}
