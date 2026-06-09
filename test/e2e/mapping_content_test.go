package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// postAndGetID POSTs a notification and returns the notification_id from response.
func postAndGetID(t *testing.T, url, body string) string {
	t.Helper()

	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])
	require.NotEmpty(t, notifID)
	return notifID
}

// ---------------------------------------------------------------------------
// TC3.7 @{payload:field} — 字段引用取值
// ---------------------------------------------------------------------------

// @test-case TC3.7-pure_field_ref
// @test-case TC3.7-nested_path
// @test-case TC3.7-missing_field
// @test-case TC3.7-non_map_intermediate
// @test-case TC3.7-static_template
// @test-case TC3.7-mixed_template
func TestMapping_FieldRef(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	mv := e2e.Vendor("mapping_vendor_19093")

	body := fmt.Sprintf(`{
		"event": "tc371.field_ref",
		"idempotent_key": "%s",
		"payload": {"order_id": "123", "a": {"b": {"c": "v"}}, "str": "hello", "id": 123}
	}
`, e2e.NewTestID("TC3.7-pure_field_ref"))

	notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

	// Wait for vendor to receive the request
	req := mv.WaitRequest(10 * time.Second)
	require.NotNil(t, req, "mapping_vendor should receive the request")

	var gotBody map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &gotBody))

	assert.Equal(t, "123", gotBody["order_id"])          // TC3.7-pure_field_ref
	assert.Equal(t, "v", gotBody["nested_val"])           // TC3.7-nested_path
	assert.Nil(t, gotBody["missing_val"])                 // TC3.7-missing_field: pure reference returns nil
	assert.Nil(t, gotBody["non_map_val"])                 // TC3.7-non_map_intermediate: pure reference returns nil
	assert.Equal(t, "static-value", gotBody["static_val"]) // TC3.7-static_template
	assert.Equal(t, "user-123", gotBody["mixed_val"])     // TC3.7-mixed_template

	status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, e2e.StatusSucceeded, status)
}

// ---------------------------------------------------------------------------
// TC3.7.2 $source — 原始类型保持
// ---------------------------------------------------------------------------

// @test-case TC3.7-source_integer
// @test-case TC3.7-source_boolean
// @test-case TC3.7-source_null
// @test-case TC3.7-source_prefix_suffix
func TestMapping_SourceDirective(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	mv := e2e.Vendor("mapping_vendor_19093")

	body := fmt.Sprintf(`{
		"event": "tc372.source",
		"idempotent_key": "%s",
		"payload": {"count": 42, "active": true, "note": null, "id": 42}
	}
`, e2e.NewTestID("TC3.7-source_integer"))

	notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

	req := mv.WaitRequest(10 * time.Second)
	require.NotNil(t, req, "mapping_vendor should receive the request")

	var gotBody map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &gotBody))

	// $source preserves original types (JSON numbers decode as float64)
	assert.Equal(t, float64(42), gotBody["count"], "integer preserved")              // TC3.7-source_integer
	assert.Equal(t, true, gotBody["active"], "boolean preserved")                     // TC3.7-source_boolean
	assert.Equal(t, nil, gotBody["note"], "null preserved")                           // TC3.7-source_null
	assert.Equal(t, "id_42", gotBody["prefixed"], "prefix+suffix → string")           // TC3.7-source_prefix_suffix

	status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, e2e.StatusSucceeded, status)
}

// ---------------------------------------------------------------------------
// TC3.7.3 $type — 强制类型转换
// ---------------------------------------------------------------------------

// @test-case TC3.7-type_int_to_string
// @test-case TC3.7-type_string_to_int
// @test-case TC3.7-type_string_to_number
// @test-case TC3.7-type_int_to_bool
func TestMapping_TypeConversion(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	mv := e2e.Vendor("mapping_vendor_19093")

	body := fmt.Sprintf(`{
		"event": "tc373.type",
		"idempotent_key": "%s",
		"payload": {"count": 42, "count_str": "42", "price": "29.99", "flag": 1}
	}
`, e2e.NewTestID("TC3.7-type_int_to_string"))

	notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

	req := mv.WaitRequest(10 * time.Second)
	require.NotNil(t, req, "mapping_vendor should receive the request")

	var gotBody map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &gotBody))

	assert.Equal(t, "42", gotBody["as_string"], "int→string")              // TC3.7-type_int_to_string

	asInt, ok := gotBody["as_int"].(float64)
	assert.True(t, ok, "as_int should be a number")
	assert.Equal(t, float64(42), asInt, "string→int")                     // TC3.7-type_string_to_int

	asNum, ok := gotBody["as_number"].(float64)
	assert.True(t, ok, "as_number should be a number")
	assert.InDelta(t, 29.99, asNum, 0.001, "string→number")               // TC3.7-type_string_to_number

	assert.Equal(t, true, gotBody["as_bool"], "int→bool")                  // TC3.7-type_int_to_bool

	status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, e2e.StatusSucceeded, status)
}

// ---------------------------------------------------------------------------
// TC3.7-type_invalid_conversion
// ---------------------------------------------------------------------------

// @test-case TC3.7-type_invalid_conversion
func TestMapping_InvalidConversion(t *testing.T) {
	t.Run("TC3.7-type_invalid_conversion", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// POST with "abc" as count — engine fails to convert "abc" to integer
		// The worker gets an error from BuildRequest and goes to dead_letter
		body := fmt.Sprintf(`{
			"event": "tc373.invalid",
			"idempotent_key": "%s",
			"payload": {"count": "abc"}
		}
`, e2e.NewTestID("TC3.7-type_invalid_conversion"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		// Notification should become FAILED since all tasks will dead-letter
		// retry_policy has max_attempts=1, so one failure → dead_letter
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusFailed, status)

		// The vendor receives 0 requests from this notification because the
		// engine fails before making any HTTP call.
		assert.Empty(t, e2e.Vendor("mapping_vendor_19093").Requests(),
			"vendor should not receive any request when mapping fails")
	})
}

// ---------------------------------------------------------------------------
// TC3.7-type_no_explicit — 无$type时使用event schema声明的类型
// ---------------------------------------------------------------------------

// @test-case TC3.7-type_no_explicit
func TestMapping_NoExplicitType(t *testing.T) {
	t.Run("TC3.7-type_no_explicit", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc373.no_explicit",
			"idempotent_key": "%s",
			"payload": {"count": 42}
		}
`, e2e.NewTestID("TC3.7-type_no_explicit"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		count, ok := gotBody["count"].(float64)
		assert.True(t, ok, "count should be a number")
		assert.Equal(t, float64(42), count)

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// ---------------------------------------------------------------------------
// TC3.7.4 $format — 格式转换
// ---------------------------------------------------------------------------

// @test-case TC3.7-format_timestamp
func TestMapping_FormatConversion(t *testing.T) {
	t.Run("TC3.7-format_timestamp", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc374.format",
			"idempotent_key": "%s",
			"payload": {"paid_at": 1716518400}
		}
`, e2e.NewTestID("TC3.7-format_timestamp"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		assert.Equal(t, "2024-05-24", gotBody["formatted_date"], "timestamp→formatted date")

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// ---------------------------------------------------------------------------
// TC3.7.5 $each — 数组遍历映射
// ---------------------------------------------------------------------------

// @test-case TC3.7-each_basic
func TestMapping_EachBasic(t *testing.T) {
	t.Run("TC3.7-each_basic", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_basic",
			"idempotent_key": "%s",
			"payload": {
				"products": [{"id": "p1", "qty": 3}, {"id": "p2", "qty": 5}]
			}
		}
`, e2e.NewTestID("TC3.7-each_basic"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "quantity": float64(3)},
			map[string]any{"product_id": "p2", "quantity": float64(5)},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_with_format
func TestMapping_EachWithFormat(t *testing.T) {
	t.Run("TC3.7-each_with_format", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_with_format",
			"idempotent_key": "%s",
			"payload": {
				"orders": [{"date": 1716518400, "total": 100}]
			}
		}
`, e2e.NewTestID("TC3.7-each_with_format"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"order_date": "2024-05-24", "amount": float64(100)},
		}
		assert.Equal(t, expected, gotBody["orders_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_with_type
func TestMapping_EachWithType(t *testing.T) {
	t.Run("TC3.7-each_with_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_with_type",
			"idempotent_key": "%s",
			"payload": {
				"items": [{"price": "29.99", "count": "3"}]
			}
		}
`, e2e.NewTestID("TC3.7-each_with_type"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		items := gotBody["items_mapped"].([]any)
		require.Len(t, items, 1)
		item := items[0].(map[string]any)
		assert.InDelta(t, 29.99, item["price"], 0.001)
		assert.Equal(t, float64(3), item["count"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_static_mixed
func TestMapping_EachStaticMixed(t *testing.T) {
	t.Run("TC3.7-each_static_mixed", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_static",
			"idempotent_key": "%s",
			"payload": {
				"products": [{"id": "p1"}]
			}
		}
`, e2e.NewTestID("TC3.7-each_static_mixed"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "source": "notification"},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_nested
func TestMapping_EachNested(t *testing.T) {
	t.Run("TC3.7-each_nested", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_nested",
			"idempotent_key": "%s",
			"payload": {
				"orders": [{"id": "o1", "items": [{"name": "apple", "price": 5}]}]
			}
		}
`, e2e.NewTestID("TC3.7-each_nested"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{
				"order_id": "o1",
				"products": []any{
					map[string]any{"product_name": "apple", "cost": float64(5)},
				},
			},
		}
		assert.Equal(t, expected, gotBody["orders_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_payload_ref
func TestMapping_EachPayloadRef(t *testing.T) {
	t.Run("TC3.7-each_payload_ref", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_payload_ref",
			"idempotent_key": "%s",
			"payload": {
				"user_id": "u_001",
				"products": [{"id": "p1", "qty": 3}]
			}
		}
`, e2e.NewTestID("TC3.7-each_payload_ref"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "quantity": float64(3), "user": "u_001"},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_empty_array
func TestMapping_EachEmptyArray(t *testing.T) {
	t.Run("TC3.7-each_empty_array", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_empty",
			"idempotent_key": "%s",
			"payload": {
				"products": []
			}
		}
`, e2e.NewTestID("TC3.7-each_empty_array"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_not_array
func TestMapping_EachNotArray(t *testing.T) {
	t.Run("TC3.7-each_not_array", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "tc375.each_not_array",
			"idempotent_key": "%s",
			"payload": {
				"products": "not_an_array"
			}
		}
`, e2e.NewTestID("TC3.7-each_not_array"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		// Notification should become FAILED because mapping fails for non-array $each source
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusFailed, status)

		// The vendor receives 0 requests from this notification because the
		// engine fails before making any HTTP call.
		assert.Empty(t, e2e.Vendor("mapping_vendor_19093").Requests(),
			"vendor should not receive any request when $each source is not an array")
	})
}

// ---------------------------------------------------------------------------
// TC3.7-each_primitive — 原始值数组映射 @{item}
// ---------------------------------------------------------------------------

// @test-case TC3.7-each_primitive
func TestMapping_EachPrimitive(t *testing.T) {
	t.Run("TC3.7-each_primitive", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_primitive",
			"idempotent_key": "%s",
			"payload": {
				"produce_list": [1, 2, 3]
			}
		}
`, e2e.NewTestID("TC3.7-each_primitive"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product": float64(1)},
			map[string]any{"product": float64(2)},
			map[string]any{"product": float64(3)},
		}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_primitive_with_type
func TestMapping_EachPrimitiveWithType(t *testing.T) {
	t.Run("TC3.7-each_primitive_with_type", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_primitive_with_type",
			"idempotent_key": "%s",
			"payload": {
				"produce_list": [1, 2, 3]
			}
		}
`, e2e.NewTestID("TC3.7-each_primitive_with_type"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product": "1"},
			map[string]any{"product": "2"},
			map[string]any{"product": "3"},
		}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_primitive_with_format
func TestMapping_EachPrimitiveWithFormat(t *testing.T) {
	t.Run("TC3.7-each_primitive_with_format", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_primitive_with_format",
			"idempotent_key": "%s",
			"payload": {
				"timestamps": [1716518400, 1716604800]
			}
		}
`, e2e.NewTestID("TC3.7-each_primitive_with_format"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"date": "2024-05-24"},
			map[string]any{"date": "2024-05-25"},
		}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// @test-case TC3.7-each_primitive_empty
func TestMapping_EachPrimitiveEmpty(t *testing.T) {
	t.Run("TC3.7-each_primitive_empty", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("mapping_vendor_19093")

		body := fmt.Sprintf(`{
			"event": "tc375.each_primitive_empty",
			"idempotent_key": "%s",
			"payload": {
				"produce_list": []
			}
		}
`, e2e.NewTestID("TC3.7-each_primitive_empty"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}
