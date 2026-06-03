package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/api/handler"
	"github.com/xnslong/rc_xnslong/internal/ingestion"
	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// ---------------------------------------------------------------------------
// Mocks for DB, MQ, Config (used to construct ingestion.Service)
// ---------------------------------------------------------------------------

type mockDB struct{ mock.Mock }

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
	return args.Get(0).([]byte), args.Error(1)
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

type mockMQ struct{ mock.Mock }

func (m *mockMQ) PublishTrigger(ctx context.Context, notificationID string) error {
	args := m.Called(ctx, notificationID)
	return args.Error(0)
}

func (m *mockMQ) PublishDelivery(ctx context.Context, vendorID, deliveryTaskID string) error {
	return nil
}

func (m *mockMQ) PublishDelayed(ctx context.Context, vendorID, deliveryTaskID string, delayMs int) error {
	return nil
}

type mockConfig struct{ mock.Mock }

func (m *mockConfig) GetVendorConfig(vendorID string) (*port.VendorConfig, bool) {
	return nil, false
}

func (m *mockConfig) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, bool) {
	return nil, false
}

func (m *mockConfig) GetRoutingRules(eventType string) []port.RoutingRule {
	return nil
}

func (m *mockConfig) GetEventSchema(eventType string) ([]byte, bool) {
	args := m.Called(eventType)
	data, _ := args.Get(0).([]byte)
	return data, args.Bool(1)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandler_Ingest_HTTPParsing(t *testing.T) {
	cfg := new(mockConfig)
	cfg.On("GetEventSchema", "order.paid").Return([]byte(`{"type":"object"}`), true)

	db := new(mockDB)
	db.On("UpsertNotification", mock.Anything, mock.Anything).Return("notif-1", true, nil)
	db.On("GetNotification", mock.Anything, "notif-1").Return(&model.Notification{
		ID: "notif-1", Status: "PENDING", EventType: "order.paid",
	}, nil)

	mq := new(mockMQ)
	mq.On("PublishTrigger", mock.Anything, "notif-1").Return(nil)

	svc := ingestion.NewService(db, mq, cfg)
	h := handler.NewHandler(svc)

	r := chi.NewRouter()
	r.Post("/api/v1/notifications", h.Ingest)

	body := `{"event":"order.paid","idempotent_key":"test-key-1","payload":{"order_id":"123"}}`
	req := httptest.NewRequest("POST", "/api/v1/notifications", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "notif-1", resp.Data["notification_id"])
	assert.Equal(t, "PENDING", resp.Data["status"])
}

func TestHandler_Ingest_Errors(t *testing.T) {
	cfg := new(mockConfig)
	cfg.On("GetEventSchema", mock.Anything).Return(nil, false)

	svc := ingestion.NewService(new(mockDB), new(mockMQ), cfg)
	h := handler.NewHandler(svc)

	r := chi.NewRouter()
	r.Post("/api/v1/notifications", h.Ingest)

	t.Run("empty body returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/notifications", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid json returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/notifications", bytes.NewReader([]byte(`{invalid`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("empty event returns 400", func(t *testing.T) {
		body := `{"event":"","payload":{"a":1}}`
		req := httptest.NewRequest("POST", "/api/v1/notifications", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestHandler_GetStatus(t *testing.T) {
	cfg := new(mockConfig)
	cfg.On("GetEventSchema", mock.Anything).Return([]byte(`{"type":"object"}`), true)

	db := new(mockDB)
	db.On("UpsertNotification", mock.Anything, mock.Anything).Return("notif-1", true, nil)

	notif := &model.Notification{
		ID: "notif-1", Status: "DELIVERING", EventType: "order.paid",
		CallerID: "system", Payload: map[string]any{"order_id": "123"},
	}
	db.On("GetNotification", mock.Anything, "notif-1").Return(notif, nil)
	db.On("GetDeliveryTasksByNotificationID", mock.Anything, "notif-1").Return([]*model.DeliveryTask{}, nil)

	mq := new(mockMQ)
	mq.On("PublishTrigger", mock.Anything, "notif-1").Return(nil)

	svc := ingestion.NewService(db, mq, cfg)
	h := handler.NewHandler(svc)

	r := chi.NewRouter()
	r.Get("/api/v1/notifications/{id}", h.GetStatus)

	req := httptest.NewRequest("GET", "/api/v1/notifications/notif-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "notif-1", resp.Data["notification_id"])
	assert.Equal(t, "DELIVERING", resp.Data["status"])
	assert.Equal(t, "order.paid", resp.Data["event"])
}

func TestHandler_GetStatusNotFound(t *testing.T) {
	cfg := new(mockConfig)

	db := new(mockDB)
	db.On("GetNotification", mock.Anything, "nonexistent").Return(nil, assert.AnError)

	svc := ingestion.NewService(db, new(mockMQ), cfg)
	h := handler.NewHandler(svc)

	r := chi.NewRouter()
	r.Get("/api/v1/notifications/{id}", h.GetStatus)

	req := httptest.NewRequest("GET", "/api/v1/notifications/nonexistent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_List(t *testing.T) {
	cfg := new(mockConfig)
	db := new(mockDB)
	db.On("ListNotifications", mock.Anything, "", "", 1, 20).Return([]*model.Notification{
		{ID: "n1", EventType: "order.paid", Status: "PENDING"},
	}, 1, nil)

	svc := ingestion.NewService(db, new(mockMQ), cfg)
	h := handler.NewHandler(svc)

	r := chi.NewRouter()
	r.Get("/api/v1/notifications", h.List)

	req := httptest.NewRequest("GET", "/api/v1/notifications", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			Total      int             `json:"total"`
			Page       int             `json:"page"`
			PageSize   int             `json:"page_size"`
			TotalPages int             `json:"total_pages"`
		} `json:"data"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Data.Total)
	assert.Equal(t, 20, resp.Data.PageSize)
	assert.Len(t, resp.Data.Items, 1)
}
