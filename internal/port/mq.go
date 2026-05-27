package port

import "context"

type MQClient interface {
	PublishTrigger(ctx context.Context, notificationID string) error
	PublishDelivery(ctx context.Context, vendorID, deliveryTaskID string) error
	PublishDelayed(ctx context.Context, vendorID, deliveryTaskID string, delayMs int) error
}
