package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// ---------------------------------------------------------------------------
// Helper: copyDir
// ---------------------------------------------------------------------------

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

// ---------------------------------------------------------------------------
// Helper: createMappingTestConfig
// ---------------------------------------------------------------------------

// createMappingTestConfig copies the pre-built tc37 testdata directory to
// a temp location and returns the path.  The directory follows DD §4.1:
//
//	testdata/tc37/
//	├── events/
//	│   └── tc37/
//	│       ├── route.yaml
//	│       └── events/        event schemas (18 files)
//	└── vendors/
//	    └── mapping_vendor/
//	        ├── vendor.yaml
//	        └── tc37/           delivery contracts (18 files)
func createMappingTestConfig(t *testing.T) string {
	t.Helper()

	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	srcDir := filepath.Join(projectRoot, "test", "e2e", "testdata", "tc37")

	tmpDir, err := os.MkdirTemp("", "e2e-mapping-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	require.NoError(t, copyDir(srcDir, tmpDir))
	return tmpDir
}

// getProjectRootMapping computes the project root from the test file location.
func getProjectRootMapping() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

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

// waitForStatusMapping polls notification status until it matches expected or timeout.
func waitForStatusMapping(baseURL, id string, expected []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v1/notifications/" + id)
		if err != nil {
			return "", err
		}
		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		status, _ := result.Data["status"].(string)
		for _, exp := range expected {
			if status == exp {
				return status, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("status not in %v after timeout", expected)
}

// ---------------------------------------------------------------------------
// TC3.7 @{payload:field} — 字段引用取值
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc371.field_ref.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc371.field_ref.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc371.field_ref.yaml](testdata/tc37/events/tc37/events/tc371.field_ref.yaml)

// @test-case TC3.7-pure_field_ref
// @test-case TC3.7-nested_path
// @test-case TC3.7-missing_field
// @test-case TC3.7-non_map_intermediate
// @test-case TC3.7-static_template
// @test-case TC3.7-mixed_template
func TestMapping_FieldRef(t *testing.T) {
	projectRoot := getProjectRootMapping()
	configDir := createMappingTestConfig(t)

	suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
	require.NoError(t, err)
	defer suite.TearDownSuite()

	mv := suite.MockVendors["mapping_vendor"]

	body := `{
		"event": "tc371.field_ref",
		"idempotent_key": "tc371-field-ref-1",
		"payload": {"order_id": "123", "a": {"b": {"c": "v"}}, "str": "hello", "id": 123}
	}`

	notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

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

	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)
}

// ---------------------------------------------------------------------------
// TC3.7.2 $source — 原始类型保持
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc372.source.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc372.source.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc372.source.yaml](testdata/tc37/events/tc37/events/tc372.source.yaml)

// @test-case TC3.7-source_integer
// @test-case TC3.7-source_boolean
// @test-case TC3.7-source_null
// @test-case TC3.7-source_prefix_suffix
func TestMapping_SourceDirective(t *testing.T) {
	projectRoot := getProjectRootMapping()
	configDir := createMappingTestConfig(t)

	suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
	require.NoError(t, err)
	defer suite.TearDownSuite()

	mv := suite.MockVendors["mapping_vendor"]

	body := `{
		"event": "tc372.source",
		"idempotent_key": "tc372-source-1",
		"payload": {"count": 42, "active": true, "note": null, "id": 42}
	}`

	notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

	req := mv.WaitRequest(10 * time.Second)
	require.NotNil(t, req, "mapping_vendor should receive the request")

	var gotBody map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &gotBody))

	// $source preserves original types (JSON numbers decode as float64)
	assert.Equal(t, float64(42), gotBody["count"], "integer preserved")              // TC3.7-source_integer
	assert.Equal(t, true, gotBody["active"], "boolean preserved")                     // TC3.7-source_boolean
	assert.Equal(t, nil, gotBody["note"], "null preserved")                           // TC3.7-source_null
	assert.Equal(t, "id_42", gotBody["prefixed"], "prefix+suffix → string")           // TC3.7-source_prefix_suffix

	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)
}

// ---------------------------------------------------------------------------
// TC3.7.3 $type — 强制类型转换
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc373.type.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc373.type.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc373.type.yaml](testdata/tc37/events/tc37/events/tc373.type.yaml)

// @test-case TC3.7-type_int_to_string
// @test-case TC3.7-type_string_to_int
// @test-case TC3.7-type_string_to_number
// @test-case TC3.7-type_int_to_bool
func TestMapping_TypeConversion(t *testing.T) {
	projectRoot := getProjectRootMapping()
	configDir := createMappingTestConfig(t)

	suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
	require.NoError(t, err)
	defer suite.TearDownSuite()

	mv := suite.MockVendors["mapping_vendor"]

	body := `{
		"event": "tc373.type",
		"idempotent_key": "tc373-type-1",
		"payload": {"count": 42, "count_str": "42", "price": "29.99", "flag": 1}
	}`

	notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

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

	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)
}

// ---------------------------------------------------------------------------
// TC3.7-type_invalid_conversion
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc373.invalid.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc373.invalid.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc373.invalid.yaml](testdata/tc37/events/tc37/events/tc373.invalid.yaml)

// @test-case TC3.7-type_invalid_conversion
func TestMapping_InvalidConversion(t *testing.T) {
	t.Run("TC3.7-type_invalid_conversion", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		// POST with "abc" as count — engine fails to convert "abc" to integer
		// The worker gets an error from BuildRequest and goes to dead_letter
		body := `{
			"event": "tc373.invalid",
			"idempotent_key": "tc373-invalid-1",
			"payload": {"count": "abc"}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		// Notification should become FAILED since all tasks will dead-letter
		// retry_policy has max_attempts=1, so one failure → dead_letter
		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"FAILED"}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", status)

		// The vendor receives 0 requests from this notification because the
		// engine fails before making any HTTP call.
		// Since each test function creates a fresh MockVendor, Requests() is empty.
		assert.Empty(t, suite.MockVendors["mapping_vendor"].Requests(),
			"vendor should not receive any request when mapping fails")
	})
}

// ---------------------------------------------------------------------------
// TC3.7-type_no_explicit — 无$type时使用event schema声明的类型
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc373.no_explicit.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc373.no_explicit.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc373.no_explicit.yaml](testdata/tc37/events/tc37/events/tc373.no_explicit.yaml)

// @test-case TC3.7-type_no_explicit
func TestMapping_NoExplicitType(t *testing.T) {
	t.Run("TC3.7-type_no_explicit", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc373.no_explicit",
			"idempotent_key": "tc373-no-explicit-1",
			"payload": {"count": 42}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		count, ok := gotBody["count"].(float64)
		assert.True(t, ok, "count should be a number")
		assert.Equal(t, float64(42), count)

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// ---------------------------------------------------------------------------
// TC3.7.4 $format — 格式转换
// ---------------------------------------------------------------------------
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc374.format.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc374.format.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc374.format.yaml](testdata/tc37/events/tc37/events/tc374.format.yaml)

// @test-case TC3.7-format_timestamp
func TestMapping_FormatConversion(t *testing.T) {
	t.Run("TC3.7-format_timestamp", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc374.format",
			"idempotent_key": "tc374-format-1",
			"payload": {"paid_at": 1716518400}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		assert.Equal(t, "2024-05-24", gotBody["formatted_date"], "timestamp→formatted date")

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// ---------------------------------------------------------------------------
// TC3.7.5 $each — 数组遍历映射
// ---------------------------------------------------------------------------

// @test-case TC3.7-each_basic
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_basic.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_basic.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_basic.yaml](testdata/tc37/events/tc37/events/tc375.each_basic.yaml)
func TestMapping_EachBasic(t *testing.T) {
	t.Run("TC3.7-each_basic", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_basic",
			"idempotent_key": "tc375-each-basic-1",
			"payload": {
				"products": [{"id": "p1", "qty": 3}, {"id": "p2", "qty": 5}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "quantity": float64(3)},
			map[string]any{"product_id": "p2", "quantity": float64(5)},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_with_format
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_with_format.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_with_format.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_with_format.yaml](testdata/tc37/events/tc37/events/tc375.each_with_format.yaml)
func TestMapping_EachWithFormat(t *testing.T) {
	t.Run("TC3.7-each_with_format", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_with_format",
			"idempotent_key": "tc375-each-format-1",
			"payload": {
				"orders": [{"date": 1716518400, "total": 100}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"order_date": "2024-05-24", "amount": float64(100)},
		}
		assert.Equal(t, expected, gotBody["orders_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_with_type
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_with_type.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_with_type.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_with_type.yaml](testdata/tc37/events/tc37/events/tc375.each_with_type.yaml)
func TestMapping_EachWithType(t *testing.T) {
	t.Run("TC3.7-each_with_type", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_with_type",
			"idempotent_key": "tc375-each-type-1",
			"payload": {
				"items": [{"price": "29.99", "count": "3"}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		items := gotBody["items_mapped"].([]any)
		require.Len(t, items, 1)
		item := items[0].(map[string]any)
		assert.InDelta(t, 29.99, item["price"], 0.001)
		assert.Equal(t, float64(3), item["count"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_static_mixed
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_static.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_static.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_static.yaml](testdata/tc37/events/tc37/events/tc375.each_static.yaml)
func TestMapping_EachStaticMixed(t *testing.T) {
	t.Run("TC3.7-each_static_mixed", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_static",
			"idempotent_key": "tc375-each-static-1",
			"payload": {
				"products": [{"id": "p1"}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "source": "notification"},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_nested
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_nested.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_nested.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_nested.yaml](testdata/tc37/events/tc37/events/tc375.each_nested.yaml)
func TestMapping_EachNested(t *testing.T) {
	t.Run("TC3.7-each_nested", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_nested",
			"idempotent_key": "tc375-each-nested-1",
			"payload": {
				"orders": [{"id": "o1", "items": [{"name": "apple", "price": 5}]}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

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

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_payload_ref
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_payload_ref.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_payload_ref.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_payload_ref.yaml](testdata/tc37/events/tc37/events/tc375.each_payload_ref.yaml)
func TestMapping_EachPayloadRef(t *testing.T) {
	t.Run("TC3.7-each_payload_ref", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_payload_ref",
			"idempotent_key": "tc375-each-payload-ref-1",
			"payload": {
				"user_id": "u_001",
				"products": [{"id": "p1", "qty": 3}]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"product_id": "p1", "quantity": float64(3), "user": "u_001"},
		}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_empty_array
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_empty.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_empty.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_empty.yaml](testdata/tc37/events/tc37/events/tc375.each_empty.yaml)
func TestMapping_EachEmptyArray(t *testing.T) {
	t.Run("TC3.7-each_empty_array", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_empty",
			"idempotent_key": "tc375-each-empty-1",
			"payload": {
				"products": []
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{}
		assert.Equal(t, expected, gotBody["products_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_not_array
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_not_array.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_not_array.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_not_array.yaml](testdata/tc37/events/tc37/events/tc375.each_not_array.yaml)
func TestMapping_EachNotArray(t *testing.T) {
	t.Run("TC3.7-each_not_array", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		body := `{
			"event": "tc375.each_not_array",
			"idempotent_key": "tc375-each-not-array-1",
			"payload": {
				"products": "not_an_array"
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		// Notification should become FAILED because mapping fails for non-array $each source
		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"FAILED"}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", status)

		// The vendor receives 0 requests from this notification because the
		// engine fails before making any HTTP call.
		assert.Empty(t, suite.MockVendors["mapping_vendor"].Requests(),
			"vendor should not receive any request when $each source is not an array")
	})
}

// ---------------------------------------------------------------------------
// TC3.7-each_primitive — 原始值数组映射 @{item}
// ---------------------------------------------------------------------------

// @test-case TC3.7-each_primitive
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_primitive.yaml](testdata/tc37/events/tc37/events/tc375.each_primitive.yaml)
func TestMapping_EachPrimitive(t *testing.T) {
	t.Run("TC3.7-each_primitive", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_primitive",
			"idempotent_key": "tc375-each-primitive-1",
			"payload": {
				"produce_list": [1, 2, 3]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

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

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_primitive_with_type
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_with_type.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_with_type.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_primitive_with_type.yaml](testdata/tc37/events/tc37/events/tc375.each_primitive_with_type.yaml)
func TestMapping_EachPrimitiveWithType(t *testing.T) {
	t.Run("TC3.7-each_primitive_with_type", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_primitive_with_type",
			"idempotent_key": "tc375-each-primitive-type-1",
			"payload": {
				"produce_list": [1, 2, 3]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

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

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_primitive_with_format
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_with_format.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_with_format.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_primitive_with_format.yaml](testdata/tc37/events/tc37/events/tc375.each_primitive_with_format.yaml)
func TestMapping_EachPrimitiveWithFormat(t *testing.T) {
	t.Run("TC3.7-each_primitive_with_format", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_primitive_with_format",
			"idempotent_key": "tc375-each-primitive-format-1",
			"payload": {
				"timestamps": [1716518400, 1716604800]
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{
			map[string]any{"date": "2024-05-24"},
			map[string]any{"date": "2024-05-25"},
		}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}

// @test-case TC3.7-each_primitive_empty
//
// Config files:
//   vendor config: [testdata/tc37/vendors/mapping_vendor/vendor.yaml](testdata/tc37/vendors/mapping_vendor/vendor.yaml)
//   contract:      [testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_empty.yaml](testdata/tc37/vendors/mapping_vendor/tc37/tc375.each_primitive_empty.yaml)
//   event schema:  [testdata/tc37/events/tc37/events/tc375.each_primitive_empty.yaml](testdata/tc37/events/tc37/events/tc375.each_primitive_empty.yaml)
func TestMapping_EachPrimitiveEmpty(t *testing.T) {
	t.Run("TC3.7-each_primitive_empty", func(t *testing.T) {
		projectRoot := getProjectRootMapping()
		configDir := createMappingTestConfig(t)

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"mapping_vendor"})
		require.NoError(t, err)
		defer suite.TearDownSuite()

		mv := suite.MockVendors["mapping_vendor"]

		body := `{
			"event": "tc375.each_primitive_empty",
			"idempotent_key": "tc375-each-primitive-empty-1",
			"payload": {
				"produce_list": []
			}
		}`

		notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)

		req := mv.WaitRequest(10 * time.Second)
		require.NotNil(t, req, "mapping_vendor should receive the request")

		var gotBody map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &gotBody))

		expected := []any{}
		assert.Equal(t, expected, gotBody["items_mapped"])

		status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status)
	})
}
