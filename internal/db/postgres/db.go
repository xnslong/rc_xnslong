package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xnslong/rc_xnslong/internal/model"
)

type Client struct {
	pool *pgxpool.Pool
}

func NewClient(dsn string) (*Client, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}
	return &Client{pool: pool}, nil
}

func (c *Client) Close() {
	c.pool.Close()
}

func (c *Client) UpsertNotification(ctx context.Context, params model.UpsertParams) (notificationID string, isNew bool, err error) {
	err = c.pool.QueryRow(ctx, `
		INSERT INTO notifications (caller_id, idempotent_key, event_type, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (caller_id, idempotent_key) DO UPDATE SET updated_at = NOW()
		RETURNING id, created_at = updated_at AS is_new
	`, params.CallerID, params.IdempotentKey, params.EventType, params.Payload).Scan(&notificationID, &isNew)
	if err != nil {
		return "", false, fmt.Errorf("upsert notification: %w", err)
	}
	return notificationID, isNew, nil
}

func (c *Client) GetNotification(ctx context.Context, id string) (*model.Notification, error) {
	n := &model.Notification{}
	err := c.pool.QueryRow(ctx, `
		SELECT id, shard_id, caller_id, idempotent_key, event_type, payload, status, created_at, updated_at
		FROM notifications WHERE id = $1
	`, id).Scan(&n.ID, &n.ShardID, &n.CallerID, &n.IdempotentKey,
		&n.EventType, &n.Payload, &n.Status, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get notification: %w", err)
	}
	return n, nil
}

func (c *Client) GetNotificationPayload(ctx context.Context, id string) (map[string]any, error) {
	var payload map[string]any
	err := c.pool.QueryRow(ctx, `SELECT payload FROM notifications WHERE id = $1`, id).Scan(&payload)
	if err != nil {
		return nil, fmt.Errorf("get notification payload: %w", err)
	}
	return payload, nil
}

func (c *Client) UpdateNotificationStatus(ctx context.Context, id, status string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE notifications SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("update notification status: %w", err)
	}
	return nil
}

func (c *Client) CreateDeliveryTasks(ctx context.Context, notificationID string, vendorIDs []string) ([]*model.DeliveryTask, error) {
	// Get event_type from the notification
	var eventType string
	err := c.pool.QueryRow(ctx, `SELECT event_type FROM notifications WHERE id = $1`, notificationID).Scan(&eventType)
	if err != nil {
		return nil, fmt.Errorf("get notification event_type: %w", err)
	}

	tasks := make([]*model.DeliveryTask, 0, len(vendorIDs))
	for _, vendorID := range vendorIDs {
		task := &model.DeliveryTask{}
		err := c.pool.QueryRow(ctx, `
			INSERT INTO delivery_tasks (notification_id, vendor_id, event_type, last_error)
			VALUES ($1, $2, $3, '')
			RETURNING id, shard_id, notification_id, vendor_id, event_type, status,
			          retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		`, notificationID, vendorID, eventType).Scan(
			&task.ID, &task.ShardID, &task.NotificationID, &task.VendorID, &task.EventType,
			&task.Status, &task.RetryCount, &task.MaxRetries, &task.NextRetryAt, &task.LastError,
			&task.CreatedAt, &task.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("insert delivery task for vendor %s: %w", vendorID, err)
		}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

func (c *Client) GetDeliveryTask(ctx context.Context, id string) (*model.DeliveryTask, error) {
	task := &model.DeliveryTask{}
	err := c.pool.QueryRow(ctx, `
		SELECT id, shard_id, notification_id, vendor_id, event_type, status,
		       retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		FROM delivery_tasks WHERE id = $1
	`, id).Scan(
		&task.ID, &task.ShardID, &task.NotificationID, &task.VendorID, &task.EventType,
		&task.Status, &task.RetryCount, &task.MaxRetries, &task.NextRetryAt, &task.LastError,
		&task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get delivery task: %w", err)
	}
	return task, nil
}

func (c *Client) UpdateDeliveryTaskStatus(ctx context.Context, id, status string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE delivery_tasks SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("update delivery task status: %w", err)
	}
	return nil
}

func (c *Client) UpdateDeliveryTaskRetry(ctx context.Context, id string, retryCount int, nextRetryAt time.Time, lastErr string) error {
	_, err := c.pool.Exec(ctx, `
		UPDATE delivery_tasks
		SET retry_count = $1, next_retry_at = $2, last_error = $3, updated_at = NOW()
		WHERE id = $4
	`, retryCount, nextRetryAt, lastErr, id)
	if err != nil {
		return fmt.Errorf("update delivery task retry: %w", err)
	}
	return nil
}

func (c *Client) InsertDeadLetter(ctx context.Context, task *model.DeliveryTask, errMsg string) error {
	_, err := c.pool.Exec(ctx, `
		UPDATE delivery_tasks
		SET status = 'DEAD_LETTER', last_error = $1, updated_at = NOW()
		WHERE id = $2
	`, errMsg, task.ID)
	if err != nil {
		return fmt.Errorf("insert dead letter: %w", err)
	}
	return nil
}

func (c *Client) GetDeliveryTaskCounts(ctx context.Context, notificationID string) (total, succeeded, deadLetter int, err error) {
	err = c.pool.QueryRow(ctx, `
		SELECT
			COUNT(*)::int AS total,
			COALESCE(SUM(CASE WHEN status = 'SUCCEEDED' THEN 1 ELSE 0 END), 0)::int AS succeeded,
			COALESCE(SUM(CASE WHEN status = 'DEAD_LETTER' THEN 1 ELSE 0 END), 0)::int AS dead_letter
		FROM delivery_tasks WHERE notification_id = $1
	`, notificationID).Scan(&total, &succeeded, &deadLetter)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("get delivery task counts: %w", err)
	}
	return total, succeeded, deadLetter, nil
}

func (c *Client) GetDeliveryTasksByNotificationID(ctx context.Context, notificationID string) ([]*model.DeliveryTask, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT id, shard_id, notification_id, vendor_id, event_type, status,
		       retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		FROM delivery_tasks WHERE notification_id = $1
		ORDER BY created_at ASC
	`, notificationID)
	if err != nil {
		return nil, fmt.Errorf("get delivery tasks by notification: %w", err)
	}
	defer rows.Close()

	var tasks []*model.DeliveryTask
	for rows.Next() {
		task := &model.DeliveryTask{}
		err := rows.Scan(
			&task.ID, &task.ShardID, &task.NotificationID, &task.VendorID, &task.EventType,
			&task.Status, &task.RetryCount, &task.MaxRetries, &task.NextRetryAt, &task.LastError,
			&task.CreatedAt, &task.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan delivery task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (c *Client) GetEventSchema(ctx context.Context, eventType string) ([]byte, error) {
	var schemaDef []byte
	err := c.pool.QueryRow(ctx, `
		SELECT schema_def FROM event_schemas
		WHERE event_type = $1 AND status = 'ACTIVE'
		ORDER BY version DESC LIMIT 1
	`, eventType).Scan(&schemaDef)
	if err != nil {
		return nil, fmt.Errorf("get event schema: %w", err)
	}
	return schemaDef, nil
}
