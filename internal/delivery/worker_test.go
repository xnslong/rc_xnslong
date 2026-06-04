package delivery_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/delivery"
	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// =============================================================================
// Mock types
// =============================================================================

// MockDBClient implements port.DBClient for testing.
type MockDBClient struct{ mock.Mock }

func (m *MockDBClient) UpsertNotification(ctx context.Context, p model.UpsertParams) (string, bool, error) {
	args := m.Called(ctx, p)
	return args.String(0), args.Bool(1), args.Error(2)
}

func (m *MockDBClient) GetNotification(ctx context.Context, id string) (*model.Notification, error) {
	args := m.Called(ctx, id)
	n, _ := args.Get(0).(*model.Notification)
	return n, args.Error(1)
}

func (m *MockDBClient) GetNotificationPayload(ctx context.Context, id string) (map[string]any, error) {
	args := m.Called(ctx, id)
	p, _ := args.Get(0).(map[string]any)
	return p, args.Error(1)
}

func (m *MockDBClient) UpdateNotificationStatus(ctx context.Context, id, status string) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

func (m *MockDBClient) CreateDeliveryTasks(ctx context.Context, notificationID string, vendorIDs []string) ([]*model.DeliveryTask, error) {
	args := m.Called(ctx, notificationID, vendorIDs)
	tasks, _ := args.Get(0).([]*model.DeliveryTask)
	return tasks, args.Error(1)
}

func (m *MockDBClient) GetDeliveryTask(ctx context.Context, id string) (*model.DeliveryTask, error) {
	args := m.Called(ctx, id)
	t, _ := args.Get(0).(*model.DeliveryTask)
	return t, args.Error(1)
}

func (m *MockDBClient) UpdateDeliveryTaskStatus(ctx context.Context, id, status string) error {
	args := m.Called(ctx, id, status)
	return args.Error(0)
}

func (m *MockDBClient) UpdateDeliveryTaskRetry(ctx context.Context, id string, retryCount int, nextRetryAt time.Time, lastErr string) error {
	args := m.Called(ctx, id, retryCount, nextRetryAt, lastErr)
	return args.Error(0)
}

func (m *MockDBClient) InsertDeadLetter(ctx context.Context, task *model.DeliveryTask, errMsg string) error {
	args := m.Called(ctx, task, errMsg)
	return args.Error(0)
}

func (m *MockDBClient) GetEventSchema(ctx context.Context, eventType string) ([]byte, error) {
	args := m.Called(ctx, eventType)
	data, _ := args.Get(0).([]byte)
	return data, args.Error(1)
}

func (m *MockDBClient) GetDeliveryTaskCounts(ctx context.Context, notificationID string) (int, int, int, error) {
	args := m.Called(ctx, notificationID)
	return args.Int(0), args.Int(1), args.Int(2), args.Error(3)
}

func (m *MockDBClient) GetDeliveryTasksByNotificationID(ctx context.Context, notificationID string) ([]*model.DeliveryTask, error) {
	args := m.Called(ctx, notificationID)
	tasks, _ := args.Get(0).([]*model.DeliveryTask)
	return tasks, args.Error(1)
}

func (m *MockDBClient) ListNotifications(ctx context.Context, callerID, event string, page, pageSize int) ([]*model.Notification, int, error) {
	args := m.Called(ctx, callerID, event, page, pageSize)
	notifs, _ := args.Get(0).([]*model.Notification)
	return notifs, args.Int(1), args.Error(2)
}

// MockMQClient implements port.MQClient for testing.
type MockMQClient struct{ mock.Mock }

func (m *MockMQClient) PublishTrigger(ctx context.Context, notificationID string) error {
	args := m.Called(ctx, notificationID)
	return args.Error(0)
}

func (m *MockMQClient) PublishDelivery(ctx context.Context, vendorID, deliveryTaskID string) error {
	args := m.Called(ctx, vendorID, deliveryTaskID)
	return args.Error(0)
}

func (m *MockMQClient) PublishDelayed(ctx context.Context, vendorID, deliveryTaskID string, delayMs int) error {
	args := m.Called(ctx, vendorID, deliveryTaskID, delayMs)
	return args.Error(0)
}

// MockConfigProvider implements port.ConfigProvider for testing.
type MockConfigProvider struct{ mock.Mock }

func (m *MockConfigProvider) GetVendorConfig(vendorID string) (*port.VendorConfig, bool) {
	args := m.Called(vendorID)
	cfg, _ := args.Get(0).(*port.VendorConfig)
	return cfg, args.Bool(1)
}

func (m *MockConfigProvider) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, bool) {
	args := m.Called(vendorID, eventType)
	spec, _ := args.Get(0).(*port.DeliverySpec)
	return spec, args.Bool(1)
}

func (m *MockConfigProvider) GetRoutingRules(eventType string) []port.RoutingRule {
	args := m.Called(eventType)
	rules, _ := args.Get(0).([]port.RoutingRule)
	return rules
}

func (m *MockConfigProvider) GetEventSchema(eventType string) ([]byte, bool) {
	args := m.Called(eventType)
	data, _ := args.Get(0).([]byte)
	return data, args.Bool(1)
}

// MockRequestBuilder implements delivery.RequestBuilder for testing.
type MockRequestBuilder struct{ mock.Mock }

func (m *MockRequestBuilder) BuildRequest(vendorCfg *port.VendorConfig, mappingCfg *port.MappingConfig, payload map[string]any) (*http.Request, error) {
	args := m.Called(vendorCfg, mappingCfg, payload)
	req, _ := args.Get(0).(*http.Request)
	return req, args.Error(1)
}

// mockRoundTripper implements http.RoundTripper for testing.
type mockRoundTripper struct{ mock.Mock }

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	args := m.Called(req)
	resp, _ := args.Get(0).(*http.Response)
	return resp, args.Error(1)
}

// =============================================================================
// Shared test data
// =============================================================================

var (
	testTaskID      = "task-delivery-001"
	testNotifID     = "notif-001"
	testVendorID    = "vendor-acme"
	testEventType   = "order.created"
	testPayload     = map[string]any{"order_id": "ORD-123", "amount": 99.99}
	testVendorCfg   = &port.VendorConfig{
		VendorID: testVendorID,
		Request: port.RequestConfig{
			Method:  "POST",
			URLTmpl: "https://vendor.example.com/api/notify",
			Headers: map[string]string{"Authorization": "Bearer test-token"},
		},
		Retry: port.RetryPolicy{
			MaxAttempts: 3,
			BaseDelayMs: 1000,
			MaxDelayMs:  30000,
			Multiplier:  2,
			Jitter:      0.1,
		},
		Judgment: port.ResponseJudgment{
			SuccessRules:   []port.ResponseRule{{JudgeType: "http_status", ExpectedStatus: 200}},
			RetryableRules: []port.ResponseRule{{JudgeType: "http_status", ExpectedStatus: 500}},
		},
	}
	testDeliverySpec = &port.DeliverySpec{
		Mapping: port.MappingConfig{
			EventType: testEventType,
			Request: port.RequestConfig{
				Method:  "POST",
				URLTmpl: "https://vendor.example.com/api/notify",
			},
			Body: port.BodyConfig{Type: "mapping", Template: map[string]any{"event": "@{payload:order_id}"}},
		},
	}
	testSuccessBody = `{"status":"ok"}`
	testErrorBody   = `{"error":"internal server error"}`
)

// =============================================================================
// Helper: create a delivery task with the given overrides.
// =============================================================================

func makeTask(overrides ...func(*model.DeliveryTask)) *model.DeliveryTask {
	t := &model.DeliveryTask{
		ID:             testTaskID,
		ShardID:        0,
		NotificationID: testNotifID,
		EventType:      testEventType,
		VendorID:       testVendorID,
		Status:         delivery.StatusDelivering,
		RetryCount:     0,
		MaxRetries:     3,
		CreatedAt:      time.Now().Add(-1 * time.Hour),
		UpdatedAt:      time.Now(),
	}
	for _, fn := range overrides {
		fn(t)
	}
	return t
}

// =============================================================================
// 6.1.1 成功投递
// =============================================================================

func TestWorker_SuccessfulDelivery(t *testing.T) {
	task := makeTask()

	// Dependencies
	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)
	mockRT := new(mockRoundTripper)

	// DB: fetch task
	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	// Config
	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	// Engine: build request succeeds
	req, err := http.NewRequest("POST", "https://vendor.example.com/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// HTTP: 200 OK
	mockRT.On("RoundTrip", mock.Anything).Return(&http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(testSuccessBody)),
		Header:     make(http.Header),
	}, nil)

	// DB: update status to SUCCEEDED
	mockDB.On("UpdateDeliveryTaskStatus", mock.Anything, testTaskID, delivery.StatusSucceeded).Return(nil)

	// DB: check notification status after success (1/1 tasks succeeded)
	mockDB.On("GetDeliveryTaskCounts", mock.Anything, testNotifID).Return(1, 1, 0, nil)
	mockDB.On("UpdateNotificationStatus", mock.Anything, testNotifID, "SUCCEEDED").Return(nil)

	// HTTP client using the mock round tripper
	httpClient := &http.Client{Transport: mockRT}

	// Worker pool
	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	err = wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err)

	mockDB.AssertExpectations(t)
	mockMQ.AssertExpectations(t) // no MQ calls expected
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockRT.AssertExpectations(t)
}

// =============================================================================
// 6.1.2 HTTP 可重试失败
// =============================================================================

func TestWorker_RetryableHTTPFailure(t *testing.T) {
	task := makeTask()

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)
	mockRT := new(mockRoundTripper)

	// DB: fetch task
	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	// Config
	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	// Engine: build request succeeds
	req, err := http.NewRequest("POST", "https://vendor.example.com/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// HTTP: 500 Internal Server Error
	mockRT.On("RoundTrip", mock.Anything).Return(&http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(bytes.NewBufferString(testErrorBody)),
		Header:     make(http.Header),
	}, nil)

	// DB: update retry (retry_count = 0 + 1 = 1)
	mockDB.On("UpdateDeliveryTaskRetry",
		mock.Anything, testTaskID, 1, mock.AnythingOfType("time.Time"), mock.Anything,
	).Return(nil)

	// MQ: publish delayed message for retry
	mockMQ.On("PublishDelayed", mock.Anything, testVendorID, testTaskID, mock.AnythingOfType("int")).Return(nil)

	httpClient := &http.Client{Transport: mockRT}

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	err = wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err) // retry scheduled → ACK

	mockDB.AssertExpectations(t)
	mockMQ.AssertExpectations(t)
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockRT.AssertExpectations(t)
}

// =============================================================================
// 6.1.3 重试超限 → 死信
// =============================================================================

func TestWorker_RetryExhaustedToDeadLetter(t *testing.T) {
	task := makeTask(func(t *model.DeliveryTask) {
		t.RetryCount = 2
		t.MaxRetries = 3 // next retry (3) >= 3 → dead letter
	})

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)
	mockRT := new(mockRoundTripper)

	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	req, err := http.NewRequest("POST", "https://vendor.example.com/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// HTTP: 500 → retry count >= max → dead letter
	mockRT.On("RoundTrip", mock.Anything).Return(&http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(bytes.NewBufferString(testErrorBody)),
		Header:     make(http.Header),
	}, nil)

	// DB: update status to DEAD_LETTER, then insert dead letter record
	mockDB.On("UpdateDeliveryTaskStatus", mock.Anything, testTaskID, delivery.StatusDeadLetter).Return(nil)
	mockDB.On("InsertDeadLetter", mock.Anything, task, mock.Anything).Return(nil)

	// DB: check notification status after dead letter (1/1 tasks dead)
	mockDB.On("GetDeliveryTaskCounts", mock.Anything, testNotifID).Return(1, 0, 1, nil)
	mockDB.On("UpdateNotificationStatus", mock.Anything, testNotifID, "FAILED").Return(nil)

	httpClient := &http.Client{Transport: mockRT}

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	err = wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err) // dead-lettered → ACK

	// Verify no delayed message was published
	mockMQ.AssertNotCalled(t, "PublishDelayed", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	mockDB.AssertExpectations(t)
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockRT.AssertExpectations(t)
}

// =============================================================================
// 6.1.4 网络错误
// =============================================================================

func TestWorker_NetworkError(t *testing.T) {
	task := makeTask()

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)
	mockRT := new(mockRoundTripper)

	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	req, err := http.NewRequest("POST", "https://vendor.example.com/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// HTTP: network error (timeout / DNS failure)
	mockRT.On("RoundTrip", mock.Anything).Return(nil, errors.New("dial tcp: lookup vendor.example.com: no such host"))

	// DB: update retry (same as retryable HTTP failure)
	mockDB.On("UpdateDeliveryTaskRetry",
		mock.Anything, testTaskID, 1, mock.AnythingOfType("time.Time"), mock.Anything,
	).Return(nil)

	// MQ: publish delayed message for retry
	mockMQ.On("PublishDelayed", mock.Anything, testVendorID, testTaskID, mock.AnythingOfType("int")).Return(nil)

	httpClient := &http.Client{Transport: mockRT}

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	err = wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err) // retry scheduled → ACK

	mockDB.AssertExpectations(t)
	mockMQ.AssertExpectations(t)
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockRT.AssertExpectations(t)
}

// =============================================================================
// 6.1.5 DB 更新失败（vendor 已成功，DB 故障）
// =============================================================================

func TestWorker_DBUpdateFailure(t *testing.T) {
	task := makeTask()

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)
	mockRT := new(mockRoundTripper)

	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	req, err := http.NewRequest("POST", "https://vendor.example.com/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// HTTP: 200 (vendor received the request)
	mockRT.On("RoundTrip", mock.Anything).Return(&http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(testSuccessBody)),
		Header:     make(http.Header),
	}, nil)

	// DB: update status fails
	mockDB.On("UpdateDeliveryTaskStatus", mock.Anything, testTaskID, delivery.StatusSucceeded).Return(errors.New("db connection lost"))

	// DB: check notification status (task still DELIVERING, not terminal)
	mockDB.On("GetDeliveryTaskCounts", mock.Anything, testNotifID).Return(1, 0, 0, nil)

	httpClient := &http.Client{Transport: mockRT}

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	err = wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err) // ACK even though DB update failed

	// Verify no retry or MQ calls were made
	mockMQ.AssertNotCalled(t, "PublishDelayed", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockDB.AssertNotCalled(t, "UpdateDeliveryTaskRetry", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockDB.AssertNotCalled(t, "UpdateDeliveryTaskStatus", mock.Anything, mock.Anything, "DEAD_LETTER")

	mockDB.AssertExpectations(t)
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockRT.AssertExpectations(t)
}

// =============================================================================
// 6.1.6 Engine 构建失败
// =============================================================================

func TestWorker_EngineBuildFailure(t *testing.T) {
	task := makeTask()

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)

	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	// Engine: build request fails
	engineErr := errors.New("build request: unsupported body type")
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(nil, engineErr)

	// Engine failure now goes through retryOrDeadLetter.
	// task.RetryCount=0, MaxAttempts=3 → schedules a retry.
	mockDB.On("UpdateDeliveryTaskRetry",
		mock.Anything, testTaskID, 1, mock.AnythingOfType("time.Time"), mock.Anything,
	).Return(nil)
	mockMQ.On("PublishDelayed", mock.Anything, testVendorID, testTaskID, mock.AnythingOfType("int")).Return(nil)

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:     mockDB,
		MQ:     mockMQ,
		Config: mockConfig,
		Engine: mockEngine,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	})

	err := wp.ProcessMessage(context.Background(), testTaskID)
	assert.NoError(t, err) // retry scheduled → ACK

	mockDB.AssertExpectations(t)
	mockMQ.AssertExpectations(t)
	mockConfig.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
}

// =============================================================================
// 6.1.7 Stop() 等待当前投递完成
// =============================================================================

func TestWorker_StopWaitsForInflight(t *testing.T) {
	// Create a slow HTTP server that takes 2s to respond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	task := makeTask()

	mockDB := new(MockDBClient)
	mockMQ := new(MockMQClient)
	mockConfig := new(MockConfigProvider)
	mockEngine := new(MockRequestBuilder)

	mockDB.On("GetDeliveryTask", mock.Anything, testTaskID).Return(task, nil)
	mockDB.On("GetNotificationPayload", mock.Anything, testNotifID).Return(testPayload, nil)

	mockConfig.On("GetVendorConfig", testVendorID).Return(testVendorCfg, true)
	mockConfig.On("GetDeliverySpec", testVendorID, testEventType).Return(testDeliverySpec, true)

	// Engine returns a request pointing to the slow test server
	req, err := http.NewRequest("POST", server.URL+"/api/notify", nil)
	require.NoError(t, err)
	mockEngine.On("BuildRequest", mock.Anything, mock.Anything, mock.Anything).Return(req, nil)

	// DB: update status succeeds
	mockDB.On("UpdateDeliveryTaskStatus", mock.Anything, testTaskID, delivery.StatusSucceeded).Return(nil)

	// DB: check notification status after success
	mockDB.On("GetDeliveryTaskCounts", mock.Anything, testNotifID).Return(1, 1, 0, nil)
	mockDB.On("UpdateNotificationStatus", mock.Anything, testNotifID, "SUCCEEDED").Return(nil)

	// Real HTTP client (no mock round tripper — hits the test server)
	httpClient := &http.Client{}

	wp := delivery.NewWorkerPool(1, delivery.WorkerDeps{
		DB:         mockDB,
		MQ:         mockMQ,
		Config:     mockConfig,
		Engine:     mockEngine,
		HTTPClient: httpClient,
	})

	ctx := context.Background()

	var processErr error
	var procWg sync.WaitGroup
	procWg.Add(1)

	// Start ProcessMessage in background — it will hit the 2s slow server
	go func() {
		defer procWg.Done()
		processErr = wp.ProcessMessage(ctx, testTaskID)
	}()

	// Give the HTTP call a moment to reach the server
	time.Sleep(100 * time.Millisecond)

	// Call Stop — should block until ProcessMessage completes
	stopStart := time.Now()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	stopErr := wp.Stop(stopCtx)
	stopElapsed := time.Since(stopStart)

	// Wait for the ProcessMessage goroutine to finish
	procWg.Wait()

	// Verify Stop() blocked for at least ~2s (the HTTP delay)
	assert.GreaterOrEqual(t, stopElapsed, 1900*time.Millisecond,
		"Stop should block until the in-flight HTTP request completes")
	assert.NoError(t, stopErr, "Stop should return without error")
	assert.NoError(t, processErr, "ProcessMessage should succeed")

	mockDB.AssertExpectations(t)
	mockEngine.AssertExpectations(t)
	mockConfig.AssertExpectations(t)

	// NOTE:
	// - Tests ①-③ cover ACK/NACK at the MQ level, which requires the
	//   port.MQClient interface to include Ack/Nack methods.
	//   The current port.MQClient only has Publish methods.
	// - Test ④ (no new messages consumed after Stop) requires Start()
	//   to be implemented, which is currently a stub.
	// - For now, ProcessMessage's return value serves as the ACK/NACK
	//   signal: nil → ACK, error → NACK (requeue).
}
