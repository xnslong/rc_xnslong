package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// apiResponse is the standard API success response wrapper.
type apiResponse struct {
	Data map[string]any `json:"data"`
}

// apiErrorResponse is the standard API error response wrapper.
type apiErrorResponse struct {
	Error struct {
		Code    string           `json:"code"`
		Message string           `json:"message"`
		Details []map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

// @test-case TC1.1-valid_payload
// Test 1.1.1: 有效提交通知 → 202 + data.notification_id
func TestIngestion_HappyPath(t *testing.T) {
	t.Run("TC1.1-valid_payload", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "ingest-happy-1",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 29900, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)

		data := result.Data
		require.NotEmpty(t, data["notification_id"])
		assert.Equal(t, "PENDING", data["status"])
		require.NotEmpty(t, data["created_at"])
	})
}

// @test-case TC1.3-duplicate_idempotent_key
// Test 1.1.2: 幂等键重复 → 同一 notification_id
func TestIngestion_DuplicateIdempotentKey(t *testing.T) {
	t.Run("TC1.3-duplicate_idempotent_key", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "ingest-dup-1",
			"payload": {"order_id": "456", "user_id": "u2", "amount": 10000, "currency": "USD"}
		}`

		// First POST
		resp1, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp1.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp1.StatusCode)

		var result1 apiResponse
		json.NewDecoder(resp1.Body).Decode(&result1)
		id1 := result1.Data["notification_id"]
		require.NotEmpty(t, id1)

		// Second POST with same payload
		resp2, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp2.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp2.StatusCode)

		var result2 apiResponse
		json.NewDecoder(resp2.Body).Decode(&result2)
		id2 := result2.Data["notification_id"]
		require.NotEmpty(t, id2)

		assert.Equal(t, id1, id2, "duplicate request should return same notification_id")
	})
}

// @test-case TC1.2-invalid_json_body
// Test 1.1.3: 无效 JSON → 400 INVALID_REQUEST
func TestIngestion_InvalidJSON(t *testing.T) {
	t.Run("TC1.2-invalid_json_body", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(`{invalid json`))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "INVALID_REQUEST", errResp.Error.Code)
	})
}

// @test-case TC1.2-empty_event
// Test 1.1.4: event 为空 → 400 INVALID_REQUEST
func TestIngestion_MissingEvent(t *testing.T) {
	t.Run("TC1.2-empty_event", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "",
			"payload": {"order_id": "123"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "INVALID_REQUEST", errResp.Error.Code)
	})
}

// @test-case TC1.4-unregistered_event
// Test 1.1.5: 事件类型未注册 → 422 EVENT_NOT_FOUND
func TestIngestion_EventTypeNotFound(t *testing.T) {
	t.Run("TC1.4-unregistered_event", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "unknown.event.type",
			"idempotent_key": "unknown-event-1",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "EVENT_NOT_FOUND", errResp.Error.Code)
	})
}

// @test-case TC1.4-missing_required_field
// Test 1.1.6: payload 不符合 Schema → 422 SCHEMA_VALIDATION_FAILED + details
func TestIngestion_SchemaValidationFailed(t *testing.T) {
	t.Run("TC1.4-missing_required_field", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// Test case: missing required field order_id
		body := `{
			"event": "order.paid",
			"idempotent_key": "schema-fail-1",
			"payload": {"user_id": "u1", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "SCHEMA_VALIDATION_FAILED", errResp.Error.Code)
		assert.NotEmpty(t, errResp.Error.Details, "should contain validation details")
	})
}

// @test-case TC1.1-auto_idempotent_key
// idempotent_key 不传自动生成
func TestIngestion_AutoIdempotentKey(t *testing.T) {
	t.Run("TC1.1-auto_idempotent_key", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"payload": {"order_id": "auto-key-1", "user_id": "u-auto", "amount": 100, "currency": "CNY"}
		}`
		// No idempotent_key in request

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		assert.NotEmpty(t, result.Data["notification_id"])
		assert.Equal(t, "PENDING", result.Data["status"])
	})
}

// @test-case TC1.2-json_array_body
// POST JSON array → 400 INVALID_REQUEST
func TestIngestion_JsonArrayBody(t *testing.T) {
	t.Run("TC1.2-json_array_body", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `[{"event": "order.paid", "payload": {"order_id": "123"}}]`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "INVALID_REQUEST", errResp.Error.Code)
	})
}

// @test-case TC1.2-json_scalar_body
// POST pure string → 400 INVALID_REQUEST
func TestIngestion_JsonScalarBody(t *testing.T) {
	t.Run("TC1.2-json_scalar_body", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(`"just a string"`))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "INVALID_REQUEST", errResp.Error.Code)
	})
}

// @test-case TC1.2-invalid_event_type
// POST event with wrong type (number) → 400 INVALID_REQUEST
func TestIngestion_InvalidEventType(t *testing.T) {
	t.Run("TC1.2-invalid_event_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{"event": 123, "idempotent_key": "tc-invalid-event-1", "payload": {"order_id": "1"}}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "INVALID_REQUEST", errResp.Error.Code)
	})
}

// @test-case TC1.4-wrong_field_type
// amount 为 string 而非 integer → 422 SCHEMA_VALIDATION_FAILED + details
func TestSchema_WrongFieldType(t *testing.T) {
	t.Run("TC1.4-wrong_field_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "tc-wrong-type-1",
			"payload": {"order_id": "123", "user_id": "u1", "amount": "not-a-number", "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "SCHEMA_VALIDATION_FAILED", errResp.Error.Code)
		assert.NotEmpty(t, errResp.Error.Details, "should contain validation details")
	})
}

// @test-case TC1.4-enum_out_of_range
// currency 为未注册的值 "GBP" → 422 SCHEMA_VALIDATION_FAILED + details
func TestSchema_EnumOutOfRange(t *testing.T) {
	t.Run("TC1.4-enum_out_of_range", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "tc-enum-1",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 100, "currency": "GBP"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "SCHEMA_VALIDATION_FAILED", errResp.Error.Code)
		assert.NotEmpty(t, errResp.Error.Details, "should contain validation details")
	})
}

// @test-case TC1.4-numeric_constraint
// amount 为 -100 违反 minimum:0 → 422 SCHEMA_VALIDATION_FAILED + details
func TestSchema_NumericConstraint(t *testing.T) {
	t.Run("TC1.4-numeric_constraint", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "tc-num-1",
			"payload": {"order_id": "123", "user_id": "u1", "amount": -100, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "SCHEMA_VALIDATION_FAILED", errResp.Error.Code)
		assert.NotEmpty(t, errResp.Error.Details, "should contain validation details")
	})
}

// @test-case TC1.4-multiple_errors
// payload 同时缺 2 个必填字段 + 类型错误 → 422 + 2+ details
func TestSchema_MultipleErrors(t *testing.T) {
	t.Run("TC1.4-multiple_errors", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := `{
			"event": "order.paid",
			"idempotent_key": "tc-multi-1",
			"payload": {"amount": "not-a-number", "currency": "CNY"}
		}`
		// Missing order_id AND user_id, plus amount type mismatch

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		var errResp apiErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		assert.Equal(t, "SCHEMA_VALIDATION_FAILED", errResp.Error.Code)
		assert.GreaterOrEqual(t, len(errResp.Error.Details), 2, "should contain 2+ validation details")
	})
}

// ---------------------------------------------------------------------------
// List Notifications API tests
// ---------------------------------------------------------------------------

func TestIngestion_ListNotifications(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	// Create a notification first
	body := `{
		"event": "order.paid",
		"idempotent_key": "list-test-1",
		"payload": {"order_id": "list1", "user_id": "u1", "amount": 100, "currency": "CNY"}
	}`

	resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	listURL := e2e.ServerURL() + "/api/v1/notifications"
	resp, err = http.Get(listURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			Total      int              `json:"total"`
			Page       int              `json:"page"`
			PageSize   int              `json:"page_size"`
			TotalPages int              `json:"total_pages"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, result.Data.Total, 1)
	assert.Equal(t, 1, result.Data.Page)
	assert.Equal(t, 20, result.Data.PageSize)
	require.Len(t, result.Data.Items, result.Data.Total)
	assert.NotEmpty(t, result.Data.Items[0]["notification_id"])
	assert.Equal(t, "order.paid", result.Data.Items[0]["event"])
}

func TestIngestion_ListNotificationsWithEventFilter(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := `{
		"event": "order.paid",
		"idempotent_key": "list-filter-test-1",
		"payload": {"order_id": "list-filter", "user_id": "u1", "amount": 100, "currency": "CNY"}
	}`

	resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	listURL := e2e.ServerURL() + "/api/v1/notifications?event=order.paid"
	resp, err = http.Get(listURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			Total      int              `json:"total"`
			Page       int              `json:"page"`
			PageSize   int              `json:"page_size"`
			TotalPages int              `json:"total_pages"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, result.Data.Total, 1)
	for _, item := range result.Data.Items {
		assert.Equal(t, "order.paid", item["event"])
	}
}

func TestIngestion_ListNotificationsWithPagination(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := `{
		"event": "order.paid",
		"idempotent_key": "list-pagination-test-1",
		"payload": {"order_id": "list-page", "user_id": "u1", "amount": 100, "currency": "CNY"}
	}`

	resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	listURL := e2e.ServerURL() + "/api/v1/notifications?page=1&page_size=1"
	resp, err = http.Get(listURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			Total      int              `json:"total"`
			Page       int              `json:"page"`
			PageSize   int              `json:"page_size"`
			TotalPages int              `json:"total_pages"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, result.Data.Total, 1)
	assert.Equal(t, 1, result.Data.Page)
	assert.Equal(t, 1, result.Data.PageSize, "page_size should be 1")
	assert.GreaterOrEqual(t, result.Data.TotalPages, 1)
}
