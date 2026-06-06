package ingestion_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/ingestion"
	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockDB struct {
	mock.Mock
}

func (m *mockDB) UpsertNotification(ctx context.Context, p model.UpsertParams) (string, bool, error) {
	args := m.Called(ctx, p)
	return args.String(0), args.Bool(1), args.Error(2)
}

func (m *mockDB) GetNotification(ctx context.Context, id string) (*model.Notification, error) {
	args := m.Called(ctx, id)
	n, _ := args.Get(0).(*model.Notification)
	return n, args.Error(1)
}

func (m *mockDB) GetNotificationPayload(ctx context.Context, id string) (map[string]any, error) {
	args := m.Called(ctx, id)
	p, _ := args.Get(0).(map[string]any)
	return p, args.Error(1)
}

func (m *mockDB) UpdateNotificationStatus(ctx context.Context, id, status string) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

func (m *mockDB) CreateDeliveryTasks(ctx context.Context, notificationID string, vendorIDs []string) ([]*model.DeliveryTask, error) {
	args := m.Called(ctx, notificationID, vendorIDs)
	tasks, _ := args.Get(0).([]*model.DeliveryTask)
	return tasks, args.Error(1)
}

func (m *mockDB) GetDeliveryTask(ctx context.Context, id string) (*model.DeliveryTask, error) {
	args := m.Called(ctx, id)
	t, _ := args.Get(0).(*model.DeliveryTask)
	return t, args.Error(1)
}

func (m *mockDB) UpdateDeliveryTaskStatus(ctx context.Context, id, status string) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

func (m *mockDB) UpdateDeliveryTaskRetry(ctx context.Context, id string, retryCount int, nextRetryAt time.Time, lastErr string) error {
	args := m.Called(ctx, id, retryCount, nextRetryAt, lastErr)
	return args.Error(0)
}

func (m *mockDB) InsertDeadLetter(ctx context.Context, task *model.DeliveryTask, errMsg string) error {
	args := m.Called(ctx, task, errMsg)
	return args.Error(0)
}

func (m *mockDB) GetEventSchema(ctx context.Context, eventType string) ([]byte, error) {
	args := m.Called(ctx, eventType)
	data, _ := args.Get(0).([]byte)
	return data, args.Error(1)
}

func (m *mockDB) GetDeliveryTaskCounts(ctx context.Context, notificationID string) (int, int, int, error) {
	args := m.Called(ctx, notificationID)
	return args.Int(0), args.Int(1), args.Int(2), args.Error(3)
}

func (m *mockDB) GetDeliveryTasksByNotificationID(ctx context.Context, notificationID string) ([]*model.DeliveryTask, error) {
	args := m.Called(ctx, notificationID)
	tasks, _ := args.Get(0).([]*model.DeliveryTask)
	return tasks, args.Error(1)
}

func (m *mockDB) ListNotifications(ctx context.Context, callerID, event string, page, pageSize int) ([]*model.Notification, int, error) {
	args := m.Called(ctx, callerID, event, page, pageSize)
	notifs, _ := args.Get(0).([]*model.Notification)
	return notifs, args.Int(1), args.Error(2)
}

type mockMQ struct {
	mock.Mock
}

func (m *mockMQ) PublishTrigger(ctx context.Context, notificationID string) error {
	args := m.Called(ctx, notificationID)
	return args.Error(0)
}

func (m *mockMQ) PublishDelivery(ctx context.Context, vendorID, deliveryTaskID string) error {
	args := m.Called(ctx, vendorID, deliveryTaskID)
	return args.Error(0)
}

func (m *mockMQ) PublishDelayed(ctx context.Context, vendorID, deliveryTaskID string, delayMs int) error {
	args := m.Called(ctx, vendorID, deliveryTaskID, delayMs)
	return args.Error(0)
}

type mockConfig struct {
	mock.Mock
}

func (m *mockConfig) GetVendorConfig(vendorID string) (*port.VendorConfig, error) {
	args := m.Called(vendorID)
	cfg, _ := args.Get(0).(*port.VendorConfig)
	return cfg, args.Error(1)
}

func (m *mockConfig) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, error) {
	args := m.Called(vendorID, eventType)
	spec, _ := args.Get(0).(*port.DeliverySpec)
	return spec, args.Error(1)
}

func (m *mockConfig) GetRoutingRules(eventType string) ([]port.RoutingRule, error) {
	args := m.Called(eventType)
	rules, _ := args.Get(0).([]port.RoutingRule)
	return rules, args.Error(1)
}

func (m *mockConfig) GetEventSchema(eventType string) (map[string]any, error) {
	args := m.Called(eventType)
	data, _ := args.Get(0).(map[string]any)
	return data, args.Error(1)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestService_SubmitHappyPath(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	eventType := "order.paid"
	payload := map[string]any{"order_id": "123"}
	schemaMap := map[string]any{"type": "object", "properties": map[string]any{"order_id": map[string]any{"type": "string"}}}

	cfg.On("GetEventSchema", eventType).Return(schemaMap, nil)

	params := model.UpsertParams{
		CallerID:      "system",
		EventType:     eventType,
		IdempotentKey: "test-key-1",
		Payload:       payload,
	}
	db.On("UpsertNotification", mock.Anything, params).Return("notif-1", true, nil)

	mq.On("PublishTrigger", mock.Anything, "notif-1").Return(nil)

	notif := &model.Notification{
		ID: "notif-1", EventType: eventType, Status: "PENDING",
	}
	db.On("GetNotification", mock.Anything, "notif-1").Return(notif, nil)

	svc := ingestion.NewService(db, mq, cfg)
	result, err := svc.Submit(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, "notif-1", result.ID)

	db.AssertExpectations(t)
	mq.AssertExpectations(t)
	cfg.AssertExpectations(t)
}

func TestService_SubmitEventNotFound(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	cfg.On("GetEventSchema", "unknown.event").Return(nil, assert.AnError)

	svc := ingestion.NewService(db, mq, cfg)
	_, err := svc.Submit(context.Background(), model.UpsertParams{
		EventType: "unknown.event", Payload: map[string]any{},
	})
	require.Error(t, err)
	assert.True(t, ingestion.IsErrEventNotFound(err))
}

func TestService_SubmitSchemaValidationFailed(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	// Schema requires order_id (string), but payload is missing it
	schemaMap := map[string]any{
		"type": "object",
		"required": []any{"order_id"},
		"properties": map[string]any{"order_id": map[string]any{"type": "string"}},
	}
	cfg.On("GetEventSchema", "order.paid").Return(schemaMap, nil)

	svc := ingestion.NewService(db, mq, cfg)
	_, err := svc.Submit(context.Background(), model.UpsertParams{
		EventType: "order.paid", IdempotentKey: "fail-1",
		Payload: map[string]any{},
	})
	require.Error(t, err)
	assert.True(t, ingestion.IsSchemaValidationError(err))

	details := ingestion.GetSchemaValidationDetails(err)
	assert.NotEmpty(t, details)
}

func TestService_SubmitIdempotentDuplicate(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	eventType := "order.paid"
	schemaMap := map[string]any{"type": "object"}
	cfg.On("GetEventSchema", eventType).Return(schemaMap, nil)

	params := model.UpsertParams{
		CallerID: "system", EventType: eventType,
		IdempotentKey: "dup-key", Payload: map[string]any{},
	}

	// First call is new
	db.On("UpsertNotification", mock.Anything, params).Return("notif-1", true, nil)
	mq.On("PublishTrigger", mock.Anything, "notif-1").Return(nil)

	notif1 := &model.Notification{ID: "notif-1", EventType: eventType, Status: "PENDING"}
	db.On("GetNotification", mock.Anything, "notif-1").Return(notif1, nil)

	svc := ingestion.NewService(db, mq, cfg)
	result1, err := svc.Submit(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, "notif-1", result1.ID)

	// Second call returns existing (isNew=false, no trigger publish)
	db.On("UpsertNotification", mock.Anything, params).Return("notif-1", false, nil)

	notif2 := &model.Notification{ID: "notif-1", EventType: eventType, Status: "PENDING"}
	db.On("GetNotification", mock.Anything, "notif-1").Return(notif2, nil)

	result2, err := svc.Submit(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, "notif-1", result2.ID)

	db.AssertExpectations(t)
	mq.AssertExpectations(t)
}

func TestService_GetByID(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	notif := &model.Notification{ID: "n1", Status: "PENDING"}
	db.On("GetNotification", mock.Anything, "n1").Return(notif, nil)

	svc := ingestion.NewService(db, mq, cfg)
	result, err := svc.GetByID(context.Background(), "n1")
	require.NoError(t, err)
	assert.Equal(t, "n1", result.ID)
}

func TestService_GetDeliveryTasks(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	tasks := []*model.DeliveryTask{
		{ID: "dt1", VendorID: "v1", Status: "DELIVERING"},
	}
	db.On("GetDeliveryTasksByNotificationID", mock.Anything, "n1").Return(tasks, nil)

	svc := ingestion.NewService(db, mq, cfg)
	result, err := svc.GetDeliveryTasks(context.Background(), "n1")
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

func TestService_ListNotifications(t *testing.T) {
	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	notifs := []*model.Notification{
		{ID: "n1", EventType: "order.paid", Status: "PENDING"},
	}
	db.On("ListNotifications", mock.Anything, "caller1", "order.paid", 1, 20).Return(notifs, 1, nil)

	svc := ingestion.NewService(db, mq, cfg)
	items, total, err := svc.List(context.Background(), "caller1", "order.paid", 1, 20)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, items, 1)
}
