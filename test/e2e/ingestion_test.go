package e2e_test

import (
	"encoding/json"
	"fmt"
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
func TestIngestion_HappyPath(t *testing.T) {
	t.Run("TC1.1-valid_payload", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 29900, "currency": "CNY"}
		}`, e2e.NewTestID("TC1.1-valid_payload"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)

		data := result.Data
		require.NotEmpty(t, data["notification_id"])
		assert.Equal(t, e2e.StatusPending, data["status"])
		require.NotEmpty(t, data["created_at"])
	})
}

// @test-case TC1.3-duplicate_idempotent_key
func TestIngestion_DuplicateIdempotentKey(t *testing.T) {
	t.Run("TC1.3-duplicate_idempotent_key", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		key := e2e.NewTestID("TC1.3-duplicate_idempotent_key")
		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "456", "user_id": "u2", "amount": 10000, "currency": "USD"}
		}`, key)

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
func TestIngestion_MissingEvent(t *testing.T) {
	t.Run("TC1.2-empty_event", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "",
			"idempotent_key": "%s",
			"payload": {"order_id": "123"}
		}`, e2e.NewTestID("TC1.2-empty_event"))

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
func TestIngestion_EventTypeNotFound(t *testing.T) {
	t.Run("TC1.4-unregistered_event", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "unknown.event.type",
			"idempotent_key": "%s",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 100, "currency": "CNY"}
		}`, e2e.NewTestID("TC1.4-unregistered_event"))

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
func TestIngestion_SchemaValidationFailed(t *testing.T) {
	t.Run("TC1.4-missing_required_field", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"user_id": "u1", "amount": 100, "currency": "CNY"}
		}`, e2e.NewTestID("TC1.4-missing_required_field"))

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
		assert.Equal(t, e2e.StatusPending, result.Data["status"])
	})
}

// @test-case TC1.2-json_array_body
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
func TestIngestion_InvalidEventType(t *testing.T) {
	t.Run("TC1.2-invalid_event_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{"event": 123, "idempotent_key": "%s", "payload": {"order_id": "1"}}`,
			e2e.NewTestID("TC1.2-invalid_event_type"))

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
func TestSchema_WrongFieldType(t *testing.T) {
	t.Run("TC1.4-wrong_field_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "123", "user_id": "u1", "amount": "not-a-number", "currency": "CNY"}
		}`, e2e.NewTestID("TC1.4-wrong_field_type"))

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
func TestSchema_EnumOutOfRange(t *testing.T) {
	t.Run("TC1.4-enum_out_of_range", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "123", "user_id": "u1", "amount": 100, "currency": "GBP"}
		}`, e2e.NewTestID("TC1.4-enum_out_of_range"))

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
func TestSchema_NumericConstraint(t *testing.T) {
	t.Run("TC1.4-numeric_constraint", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "123", "user_id": "u1", "amount": -100, "currency": "CNY"}
		}`, e2e.NewTestID("TC1.4-numeric_constraint"))

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
func TestSchema_MultipleErrors(t *testing.T) {
	t.Run("TC1.4-multiple_errors", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"amount": "not-a-number", "currency": "CNY"}
		}`, e2e.NewTestID("TC1.4-multiple_errors"))

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

// @test-case TC2.3-list-with-event-filter
func TestIngestion_ListNotificationsWithEventFilter(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := fmt.Sprintf(`{
		"event": "order.paid",
		"idempotent_key": "%s",
		"payload": {"order_id": "list-filter", "user_id": "u1", "amount": 100, "currency": "CNY"}
	}`, e2e.NewTestID("TC2-list-filter"))

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

// @test-case TC2.4-list-with-pagination
func TestIngestion_ListNotificationsWithPagination(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := fmt.Sprintf(`{
		"event": "order.paid",
		"idempotent_key": "%s",
		"payload": {"order_id": "list-page", "user_id": "u1", "amount": 100, "currency": "CNY"}
	}`, e2e.NewTestID("TC2-list-pagination"))

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
