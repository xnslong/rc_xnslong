package routing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
	"github.com/xnslong/rc_xnslong/internal/routing"
)

// ---------------------------------------------------------------------------
// Mock implementations
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
	notif, _ := args.Get(0).(*model.Notification)
	return notif, args.Error(1)
}

func (m *mockDB) GetNotificationPayload(ctx context.Context, id string) (map[string]any, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(map[string]any), args.Error(1)
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
	task, _ := args.Get(0).(*model.DeliveryTask)
	return task, args.Error(1)
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
// Test cases
// ---------------------------------------------------------------------------

// TestDispatcher_MultipleRules covers:
//   Given GetRoutingRules returns 2 rules,
//   When Dispatch is called,
//   Then 2 delivery tasks are created and 2 MQ messages are published.
//
// Corresponds to test case 4.1.1 in the test plan.
func TestDispatcher_MultipleRules(t *testing.T) {
	t.Parallel()

	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	// --- Arrange ---

	// Load the notification (PENDING so it is not skipped)
	notif := &model.Notification{
		ID:        "n1",
		EventType: "order.paid",
		Status:    "PENDING",
	}
	db.On("GetNotification", mock.Anything, "n1").Return(notif, nil)

	// Two routing rules match "order.paid"
	rules := []port.RoutingRule{
		{EventType: "order.paid", VendorID: "crm_system"},
		{EventType: "order.paid", VendorID: "ad_platform"},
	}
	cfg.On("GetRoutingRules", "order.paid").Return(rules, nil)

	// Create 2 delivery tasks
	tasks := []*model.DeliveryTask{
		{ID: "dt1", NotificationID: "n1", VendorID: "crm_system", EventType: "order.paid"},
		{ID: "dt2", NotificationID: "n1", VendorID: "ad_platform", EventType: "order.paid"},
	}
	db.On("CreateDeliveryTasks", mock.Anything, "n1", []string{"crm_system", "ad_platform"}).Return(tasks, nil)

	// Update notification status to DELIVERING
	db.On("UpdateNotificationStatus", mock.Anything, "n1", "DELIVERING").Return(nil)

	// Publish each delivery message
	mq.On("PublishDelivery", mock.Anything, "crm_system", "dt1").Return(nil)
	mq.On("PublishDelivery", mock.Anything, "ad_platform", "dt2").Return(nil)

	// --- Act ---

	d := routing.NewDispatcher(db, mq, cfg)
	err := d.Dispatch(context.Background(), "n1")

	// --- Assert ---

	require.NoError(t, err)

	db.AssertExpectations(t)
	mq.AssertExpectations(t)
	cfg.AssertExpectations(t)

	// Explicitly verify that PublishDelivery was called twice
	mq.AssertNumberOfCalls(t, "PublishDelivery", 2)
}

// TestDispatcher_NoRules covers:
//   Given GetRoutingRules returns 0 rules,
//   When Dispatch is called,
//   Then UpdateNotificationStatus("n1", "FAILED") is called and no tasks are created.
//
// Corresponds to test case 4.1.2 in the test plan.
func TestDispatcher_NoRules(t *testing.T) {
	t.Parallel()

	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	// --- Arrange ---

	notif := &model.Notification{
		ID:        "n1",
		EventType: "order.paid",
		Status:    "PENDING",
	}
	db.On("GetNotification", mock.Anything, "n1").Return(notif, nil)

	// No rules match "order.paid"
	cfg.On("GetRoutingRules", "order.paid").Return([]port.RoutingRule{}, nil)

	// Should update notification status to FAILED
	db.On("UpdateNotificationStatus", mock.Anything, "n1", "FAILED").Return(nil)

	// --- Act ---

	d := routing.NewDispatcher(db, mq, cfg)
	err := d.Dispatch(context.Background(), "n1")

	// --- Assert ---

	require.NoError(t, err)

	db.AssertExpectations(t)
	cfg.AssertExpectations(t)
}

// TestDispatcher_AlreadyDelivering covers:
//   Given a notification with status DELIVERING (already processed),
//   When Dispatch is called,
//   Then processing is skipped entirely — no routing rules are fetched,
//   no delivery tasks are created, and no MQ messages are published.
//
// Corresponds to test case 4.1.3 in the test plan.
func TestDispatcher_AlreadyDelivering(t *testing.T) {
	t.Parallel()

	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	// --- Arrange ---

	// Notification already has a non-PENDING status → skip processing
	notif := &model.Notification{
		ID:        "n1",
		EventType: "order.paid",
		Status:    "DELIVERING",
	}
	db.On("GetNotification", mock.Anything, "n1").Return(notif, nil)

	// Do NOT set up GetRoutingRules or CreateDeliveryTasks —
	// if they are called, the mock will panic and the test fails.

	// --- Act ---

	d := routing.NewDispatcher(db, mq, cfg)
	err := d.Dispatch(context.Background(), "n1")

	// --- Assert ---

	require.NoError(t, err)

	db.AssertExpectations(t)

	// Verify that no further DB/MQ/Config calls were made
	db.AssertNotCalled(t, "CreateDeliveryTasks")
	mq.AssertNotCalled(t, "PublishDelivery")
	cfg.AssertNotCalled(t, "GetRoutingRules")
}

// TestDispatcher_DBCreateError covers:
//   Given CreateDeliveryTasks returns an error,
//   When Dispatch is called,
//   Then the error is propagated and no MQ messages are published.
//
// Corresponds to test case 4.1.4 in the test plan.
func TestDispatcher_DBCreateError(t *testing.T) {
	t.Parallel()

	db := new(mockDB)
	mq := new(mockMQ)
	cfg := new(mockConfig)

	// --- Arrange ---

	notif := &model.Notification{
		ID:        "n1",
		EventType: "order.paid",
		Status:    "PENDING",
	}
	db.On("GetNotification", mock.Anything, "n1").Return(notif, nil)

	rules := []port.RoutingRule{
		{EventType: "order.paid", VendorID: "crm_system"},
	}
	cfg.On("GetRoutingRules", "order.paid").Return(rules, nil)

	// DB returns an error when creating tasks
	expectedErr := errors.New("db insert error")
	db.On("CreateDeliveryTasks", mock.Anything, "n1", []string{"crm_system"}).Return(nil, expectedErr)

	// --- Act ---

	d := routing.NewDispatcher(db, mq, cfg)
	err := d.Dispatch(context.Background(), "n1")

	// --- Assert ---

	require.Error(t, err)
	assert.ErrorContains(t, err, "db insert error")

	db.AssertExpectations(t)
	cfg.AssertExpectations(t)

	// No MQ messages should be published when DB fails
	mq.AssertNotCalled(t, "PublishDelivery")
}
