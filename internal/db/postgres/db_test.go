//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xnslong/rc_xnslong/internal/model"
)

var (
	testClient *Client
	testPool   *pgxpool.Pool
)

func TestMain(m *testing.M) {
	containerID, dsn, err := startPostgres()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to start postgres: %v\n", err)
		os.Exit(1)
	}
	defer stopContainer(containerID)

	if err := runMigrations(dsn); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create pool: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	client, err := NewClient(dsn)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create client: %v\n", err)
		os.Exit(1)
	}
	testClient = client

	code := m.Run()
	testClient.Close()
	testPool.Close()
	os.Exit(code)
}

// --- docker helpers ---

func startPostgres() (containerID, dsn string, err error) {
	cmd := exec.Command("docker", "run", "-d", "-P",
		"-e", "POSTGRES_USER=notify",
		"-e", "POSTGRES_PASSWORD=notify",
		"-e", "POSTGRES_DB=notification",
		"postgres:16-alpine",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("docker run: %w", err)
	}
	containerID = strings.TrimSpace(string(out))

	hostPort, err := getHostPort(containerID, "5432/tcp")
	if err != nil {
		stopContainer(containerID)
		return "", "", fmt.Errorf("get host port: %w", err)
	}

	dsn = fmt.Sprintf("postgres://notify:notify@localhost:%s/notification?sslmode=disable", hostPort)

	if err := waitForPostgres(dsn, 30*time.Second); err != nil {
		stopContainer(containerID)
		return "", "", err
	}

	return containerID, dsn, nil
}

func getHostPort(containerID, containerPort string) (string, error) {
	out, err := exec.Command("docker", "port", containerID, containerPort).Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	idx := strings.LastIndex(lines[0], ":")
	if idx < 0 {
		return "", fmt.Errorf("unexpected docker port output: %s", lines[0])
	}
	return lines[0][idx+1:], nil
}

func stopContainer(id string) {
	_ = exec.Command("docker", "rm", "-f", id).Run()
}

func waitForPostgres(dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var result int
		if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result); err == nil {
			pool.Close()
			return nil
		}
		pool.Close()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres not ready within %v", timeout)
}

func runMigrations(dsn string) error {
	migrationsDir := findMigrationsDir()
	files := []string{"001_init.up.sql", "002_event_schemas.up.sql"}
	for _, file := range files {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, file))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file, err)
		}
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return fmt.Errorf("connect for migration %s: %w", file, err)
		}
		if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
			pool.Close()
			return fmt.Errorf("exec migration %s: %w", file, err)
		}
		pool.Close()
	}
	return nil
}

func findMigrationsDir() string {
	candidates := []string{
		"../../../migrations",
		"migrations",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return "migrations"
}

func cleanDB(t *testing.T) {
	_, err := testPool.Exec(context.Background(),
		"TRUNCATE delivery_tasks, notifications, event_schemas CASCADE")
	require.NoError(t, err)
}

// --- tests ---

func TestClient_UpsertNotification(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("new notification creates and returns ID with isNew=true", func(t *testing.T) {
		id, isNew, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-a",
			IdempotentKey: "key-001",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "123"},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, id)
		assert.True(t, isNew)
	})

	t.Run("duplicate idempotent_key returns same ID with isNew=false", func(t *testing.T) {
		id1, _, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-a",
			IdempotentKey: "key-002",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "456"},
		})
		require.NoError(t, err)

		id2, isNew, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-a",
			IdempotentKey: "key-002",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "789"},
		})
		require.NoError(t, err)
		assert.Equal(t, id1, id2)
		assert.False(t, isNew)
	})

	t.Run("different caller same idempotent_key creates separate record", func(t *testing.T) {
		id1, _, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-b",
			IdempotentKey: "key-003",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "111"},
		})
		require.NoError(t, err)

		id2, isNew, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-c",
			IdempotentKey: "key-003",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "222"},
		})
		require.NoError(t, err)
		assert.NotEqual(t, id1, id2)
		assert.True(t, isNew)
	})
}

func TestClient_GetNotification(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("existing id returns notification with all fields", func(t *testing.T) {
		id, _, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-get",
			IdempotentKey: "key-get-001",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "999"},
		})
		require.NoError(t, err)

		n, err := testClient.GetNotification(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, id, n.ID)
		assert.Equal(t, "caller-get", n.CallerID)
		assert.Equal(t, "key-get-001", n.IdempotentKey)
		assert.Equal(t, "order.created", n.EventType)
		assert.Equal(t, "PENDING", n.Status)
		assert.NotZero(t, n.CreatedAt)
		assert.NotZero(t, n.UpdatedAt)
	})

	t.Run("non-existent id returns error", func(t *testing.T) {
		_, err := testClient.GetNotification(ctx, "00000000-0000-0000-0000-000000000000")
		require.Error(t, err)
	})
}

func TestClient_GetNotificationPayload(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("existing id returns payload only", func(t *testing.T) {
		id, _, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-payload",
			IdempotentKey: "key-payload-001",
			EventType:     "order.created",
			Payload:       map[string]any{"order_id": "777", "amount": 100},
		})
		require.NoError(t, err)

		payload, err := testClient.GetNotificationPayload(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "777", payload["order_id"])
		assert.Equal(t, float64(100), payload["amount"]) // JSONB number decodes as float64
	})
}

// @test-case 3.1.4
func TestClient_UpdateNotificationStatus(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("update status succeeds", func(t *testing.T) {
		id, _, err := testClient.UpsertNotification(ctx, model.UpsertParams{
			CallerID:      "caller-status",
			IdempotentKey: "key-status-001",
			EventType:     "order.created",
			Payload:       map[string]any{},
		})
		require.NoError(t, err)

		err = testClient.UpdateNotificationStatus(ctx, id, "SUCCEEDED")
		require.NoError(t, err)

		n, err := testClient.GetNotification(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", n.Status)
	})
}

func TestClient_CreateDeliveryTasks(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	notificationID := insertTestNotification(t, "caller-dt", "key-dt-001")

	t.Run("single vendor creates one task", func(t *testing.T) {
		tasks, err := testClient.CreateDeliveryTasks(ctx, notificationID, []string{"vendor-a"})
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		assert.Equal(t, notificationID, tasks[0].NotificationID)
		assert.Equal(t, "vendor-a", tasks[0].VendorID)
		assert.Equal(t, "order.created", tasks[0].EventType)
		assert.Equal(t, "PENDING", tasks[0].Status)
		assert.Equal(t, 5, tasks[0].MaxRetries)
		assert.Equal(t, 0, tasks[0].RetryCount)
	})

	t.Run("multiple vendors creates multiple tasks", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-dt2", "key-dt-002")

		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID, []string{"vendor-a", "vendor-b", "vendor-c"})
		require.NoError(t, err)
		require.Len(t, tasks, 3)
		for _, task := range tasks {
			assert.Equal(t, notifID, task.NotificationID)
			assert.Equal(t, "order.created", task.EventType)
		}
	})

	t.Run("non-existent notification returns error", func(t *testing.T) {
		_, err := testClient.CreateDeliveryTasks(ctx, "00000000-0000-0000-0000-000000000000", []string{"vendor-a"})
		require.Error(t, err)
	})
}

func TestClient_GetDeliveryTask(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("existing id returns task with all fields", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-gdt", "key-gdt-001")
		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID, []string{"vendor-a"})
		require.NoError(t, err)
		taskID := tasks[0].ID

		task, err := testClient.GetDeliveryTask(ctx, taskID)
		require.NoError(t, err)
		assert.Equal(t, taskID, task.ID)
		assert.Equal(t, notifID, task.NotificationID)
		assert.Equal(t, "vendor-a", task.VendorID)
		assert.Equal(t, "order.created", task.EventType)
		assert.Equal(t, "PENDING", task.Status)
		assert.Equal(t, 0, task.RetryCount)
		assert.Equal(t, 5, task.MaxRetries)
	})
}

func TestClient_UpdateDeliveryTaskStatus(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("update status succeeds", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-uds", "key-uds-001")
		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID, []string{"vendor-a"})
		require.NoError(t, err)
		taskID := tasks[0].ID

		err = testClient.UpdateDeliveryTaskStatus(ctx, taskID, "SUCCEEDED")
		require.NoError(t, err)

		task, err := testClient.GetDeliveryTask(ctx, taskID)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", task.Status)
	})
}

func TestClient_UpdateDeliveryTaskRetry(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("update retry count and next_retry_at succeeds", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-udr", "key-udr-001")
		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID, []string{"vendor-a"})
		require.NoError(t, err)
		taskID := tasks[0].ID

		nextRetry := time.Now().Add(5 * time.Minute)
		err = testClient.UpdateDeliveryTaskRetry(ctx, taskID, 1, nextRetry, "timeout")
		require.NoError(t, err)

		task, err := testClient.GetDeliveryTask(ctx, taskID)
		require.NoError(t, err)
		assert.Equal(t, 1, task.RetryCount)
		assert.True(t, task.NextRetryAt.Equal(nextRetry) ||
			task.NextRetryAt.Sub(nextRetry) < time.Second)
		assert.Equal(t, "timeout", task.LastError)
	})
}

func TestClient_InsertDeadLetter(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("update task to DEAD_LETTER with error message", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-dl", "key-dl-001")
		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID, []string{"vendor-a"})
		require.NoError(t, err)
		task := tasks[0]

		err = testClient.InsertDeadLetter(ctx, task, "max retries exceeded")
		require.NoError(t, err)

		updated, err := testClient.GetDeliveryTask(ctx, task.ID)
		require.NoError(t, err)
		assert.Equal(t, "DEAD_LETTER", updated.Status)
		assert.Equal(t, "max retries exceeded", updated.LastError)
	})
}

func TestClient_GetDeliveryTaskCounts(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	t.Run("returns correct counts for mixed status tasks", func(t *testing.T) {
		notifID := insertTestNotification(t, "caller-gdtc", "key-gdtc-001")
		tasks, err := testClient.CreateDeliveryTasks(ctx, notifID,
			[]string{"vendor-a", "vendor-b", "vendor-c", "vendor-d"})
		require.NoError(t, err)
		require.Len(t, tasks, 4)

		require.NoError(t, testClient.UpdateDeliveryTaskStatus(ctx, tasks[0].ID, "SUCCEEDED"))
		require.NoError(t, testClient.UpdateDeliveryTaskStatus(ctx, tasks[1].ID, "SUCCEEDED"))
		require.NoError(t, testClient.InsertDeadLetter(ctx, tasks[2], "dead"))

		total, succeeded, deadLetter, err := testClient.GetDeliveryTaskCounts(ctx, notifID)
		require.NoError(t, err)
		assert.Equal(t, 4, total)
		assert.Equal(t, 2, succeeded)
		assert.Equal(t, 1, deadLetter)
	})
}

// @test-case 3.1
func TestClient_GetEventSchema(t *testing.T) {
	cleanDB(t)
	ctx := context.Background()

	// seed an event schema
	schemaDef := `{"type": "object", "properties": {"order_id": {"type": "string"}}}`
	_, err := testPool.Exec(ctx, `
		INSERT INTO event_schemas (event_type, version, schema_def, description)
		VALUES ($1, 1, $2, 'test schema')
	`, "order.created", schemaDef)
	require.NoError(t, err)

	t.Run("existing event_type returns schema", func(t *testing.T) {
		schema, err := testClient.GetEventSchema(ctx, "order.created")
		require.NoError(t, err)
		require.NotNil(t, schema)
		assert.Contains(t, string(schema), "order_id")
	})

	t.Run("non-existent event_type returns error", func(t *testing.T) {
		_, err := testClient.GetEventSchema(ctx, "unknown.event")
		require.Error(t, err)
	})
}

// --- helpers ---

func insertTestNotification(t *testing.T, callerID, idempotentKey string) string {
	id, _, err := testClient.UpsertNotification(context.Background(), model.UpsertParams{
		CallerID:      callerID,
		IdempotentKey: idempotentKey,
		EventType:     "order.created",
		Payload:       map[string]any{"test": true},
	})
	require.NoError(t, err)
	return id
}
