package routing

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// Dispatcher consumes trigger messages, matches routing rules, creates delivery tasks,
// and publishes delivery messages to MQ.
//
// MVP scope: simple event-to-vendor mapping only, no conditional routing.
type Dispatcher struct {
	db  port.DBClient
	mq  port.MQClient
	cfg port.ConfigProvider
}

// NewDispatcher creates a new routing dispatcher.
func NewDispatcher(db port.DBClient, mq port.MQClient, cfg port.ConfigProvider) *Dispatcher {
	return &Dispatcher{
		db:  db,
		mq:  mq,
		cfg: cfg,
	}
}

// Dispatch processes a single notification ID: loads the notification, matches
// routing rules by event type, creates delivery tasks, and publishes delivery
// messages for each matched vendor.
func (d *Dispatcher) Dispatch(ctx context.Context, notificationID string) error {
	log := zerolog.Ctx(ctx)

	// 1. Load the notification
	notif, err := d.db.GetNotification(ctx, notificationID)
	if err != nil {
		log.Error().Err(err).Str("notification_id", notificationID).Msg("failed to load notification for dispatch")
		return err
	}

	// 2. Skip if the notification is not pending (idempotency)
	if notif.Status != "PENDING" {
		log.Info().Str("notification_id", notificationID).Str("status", notif.Status).Msg("notification already processed, skipping dispatch")
		return nil
	}

	// 3. Match routing rules by event type
	rules, err := d.cfg.GetRoutingRules(notif.EventType)
	if err != nil || len(rules) == 0 {
		log.Warn().Str("notification_id", notificationID).Str("event_type", notif.EventType).Msg("no routing rules matched, marking notification as FAILED")
		if err := d.db.UpdateNotificationStatus(ctx, notificationID, "FAILED"); err != nil {
			log.Error().Err(err).Str("notification_id", notificationID).Msg("failed to update notification status to FAILED")
			return err
		}
		return nil
	}

	// 5. Extract vendor IDs from matching rules
	vendorIDs := make([]string, len(rules))
	for i, rule := range rules {
		vendorIDs[i] = rule.VendorID
	}

	log.Info().Str("notification_id", notificationID).Strs("vendor_ids", vendorIDs).Int("count", len(vendorIDs)).Msg("creating delivery tasks for matched vendors")

	// 6. Create delivery tasks
	tasks, err := d.db.CreateDeliveryTasks(ctx, notificationID, vendorIDs)
	if err != nil {
		log.Error().Err(err).Str("notification_id", notificationID).Msg("failed to create delivery tasks")
		return err
	}

	// 6.5 Update notification status to DELIVERING
	if err := d.db.UpdateNotificationStatus(ctx, notificationID, "DELIVERING"); err != nil {
		log.Error().Err(err).Str("notification_id", notificationID).Msg("failed to update notification status to DELIVERING")
		return err
	}

	// 7. Publish delivery message for each created task
	for _, task := range tasks {
		log.Info().Str("delivery_task_id", task.ID).Str("vendor_id", task.VendorID).Msg("publishing delivery message")
		if err := d.mq.PublishDelivery(ctx, task.VendorID, task.ID); err != nil {
			log.Error().Err(err).Str("delivery_task_id", task.ID).Str("vendor_id", task.VendorID).Msg("failed to publish delivery message")
			return err
		}
	}

	return nil
}
