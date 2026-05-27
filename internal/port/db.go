package port

import (
	"context"
	"time"

	"github.com/xnslong/rc_xnslong/internal/model"
)

type DBClient interface {
	UpsertNotification(ctx context.Context, p model.UpsertParams) (notificationID string, isNew bool, err error)
	GetNotification(ctx context.Context, id string) (*model.Notification, error)
	GetNotificationPayload(ctx context.Context, id string) (map[string]any, error)
	UpdateNotificationStatus(ctx context.Context, id, status string) error

	CreateDeliveryTasks(ctx context.Context, notificationID string, vendorIDs []string) ([]*model.DeliveryTask, error)
	GetDeliveryTask(ctx context.Context, id string) (*model.DeliveryTask, error)
	UpdateDeliveryTaskStatus(ctx context.Context, id, status string) error
	UpdateDeliveryTaskRetry(ctx context.Context, id string, retryCount int, nextRetryAt time.Time, lastErr string) error

	InsertDeadLetter(ctx context.Context, task *model.DeliveryTask, errMsg string) error

	GetDeliveryTaskCounts(ctx context.Context, notificationID string) (total, succeeded, deadLetter int, err error)
	GetDeliveryTasksByNotificationID(ctx context.Context, notificationID string) ([]*model.DeliveryTask, error)

	GetEventSchema(ctx context.Context, eventType string) ([]byte, error)
}
