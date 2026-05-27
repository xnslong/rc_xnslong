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
// Helper: createMappingTestConfig
// ---------------------------------------------------------------------------

// createMappingTestConfig creates a temp config directory with a dedicated
// vendor, event schemas, routing rules, and mapping files for TC3.7 tests.
func createMappingTestConfig(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "e2e-mapping-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Directory structure
	for _, d := range []string{"vendors", "mappings/mapping_vendor", "event_schemas"} {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, d), 0755))
	}

	// Vendor YAML
	vendorYAML := `vendor_id: "mapping_vendor"
request:
  method: POST
  url: "http://localhost:19093/api/notify"
  headers:
    Content-Type: "application/json"
  body:
    type: mapping
retry_policy:
  max_attempts: 1
  base_delay: 1s
  max_delay: 5s
  multiplier: 2.0
  jitter: 0.2
response_judgment:
  success:
    type: http_status
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "vendors", "mapping_vendor.yaml"), []byte(vendorYAML), 0644))

	// Routing rules
	routingYAML := `rules:
  - event_type: "tc371.field_ref"
    vendor_id: "mapping_vendor"
  - event_type: "tc372.source"
    vendor_id: "mapping_vendor"
  - event_type: "tc373.type"
    vendor_id: "mapping_vendor"
  - event_type: "tc374.format"
    vendor_id: "mapping_vendor"
  - event_type: "tc373.invalid"
    vendor_id: "mapping_vendor"
  - event_type: "tc373.no_explicit"
    vendor_id: "mapping_vendor"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "routing_rules.yaml"), []byte(routingYAML), 0644))

	// Event schemas (minimal — accept any object)
	for _, eventType := range []string{"tc371.field_ref", "tc372.source", "tc373.type", "tc374.format", "tc373.invalid", "tc373.no_explicit"} {
		schemaYAML := fmt.Sprintf(`event_type: %q
schema:
  type: object
`, eventType)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "event_schemas", eventType+".yaml"), []byte(schemaYAML), 0644))
	}

	// Override schema for tc373.no_explicit — declare count as integer
	schemaNoExplicit := `event_type: "tc373.no_explicit"
schema:
  type: object
  properties:
    count:
      type: integer
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "event_schemas", "tc373.no_explicit.yaml"), []byte(schemaNoExplicit), 0644))

	// Mapping files
	// TC3.7.1 — @{payload.field} field references
	mapping371 := `event_type: "tc371.field_ref"
request:
  body:
    type: mapping
    template:
      order_id: "@{payload.order_id}"
      nested_val: "@{payload.a.b.c}"
      missing_val: "@{payload.missing}"
      non_map_val: "@{payload.str.x}"
      static_val: "static-value"
      mixed_val: "user-@{payload.id}"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc371.field_ref.yaml"), []byte(mapping371), 0644))

	// TC3.7.2 — $source original type preservation
	mapping372 := `event_type: "tc372.source"
request:
  body:
    type: mapping
    template:
      count:
        $source: "@{payload.count}"
      active:
        $source: "@{payload.active}"
      note:
        $source: "@{payload.note}"
      prefixed:
        $source: "id_@{payload.id}"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc372.source.yaml"), []byte(mapping372), 0644))

	// TC3.7.3 — $type force type conversion
	mapping373 := `event_type: "tc373.type"
request:
  body:
    type: mapping
    template:
      as_string:
        $source: "@{payload.count}"
        $type: string
      as_int:
        $source: "@{payload.count_str}"
        $type: integer
      as_number:
        $source: "@{payload.price}"
        $type: number
      as_bool:
        $source: "@{payload.flag}"
        $type: boolean
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc373.type.yaml"), []byte(mapping373), 0644))

	// TC3.7.4 — $format timestamp conversion
	mapping374 := `event_type: "tc374.format"
request:
  body:
    type: mapping
    template:
      formatted_date:
        $source: "@{payload.paid_at}"
        $format: "2006-01-02"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc374.format.yaml"), []byte(mapping374), 0644))

	// TC3.7-invalid_conversion — intentionally invalid type conversion
	mappingInvalid := `event_type: "tc373.invalid"
request:
  body:
    type: mapping
    template:
      invalid:
        $source: "@{payload.count}"
        $type: integer
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc373.invalid.yaml"), []byte(mappingInvalid), 0644))

	// TC3.7-type_no_explicit — no $type, uses event schema declared type
	mappingNoExplicit := `event_type: "tc373.no_explicit"
request:
  body:
    type: mapping
    template:
      count:
        $source: "@{payload.count}"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mappings", "mapping_vendor", "tc373.no_explicit.yaml"), []byte(mappingNoExplicit), 0644))

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
// TC3.7.1 @{payload.field} — 字段引用取值
// ---------------------------------------------------------------------------

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
	assert.Equal(t, "", gotBody["missing_val"])           // TC3.7-missing_field
	assert.Equal(t, "", gotBody["non_map_val"])           // TC3.7-non_map_intermediate
	assert.Equal(t, "static-value", gotBody["static_val"]) // TC3.7-static_template
	assert.Equal(t, "user-123", gotBody["mixed_val"])     // TC3.7-mixed_template

	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)
}

// ---------------------------------------------------------------------------
// TC3.7.2 $source — 原始类型保持
// ---------------------------------------------------------------------------

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

// @test-case TC3.7-type_invalid_conversion
func TestMapping_InvalidConversion(t *testing.T) {
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
}

// ---------------------------------------------------------------------------
// TC3.7-type_no_explicit — 无$type时使用event schema声明的类型
// ---------------------------------------------------------------------------

// @test-case TC3.7-type_no_explicit
func TestMapping_NoExplicitType(t *testing.T) {
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
}

// ---------------------------------------------------------------------------
// TC3.7.4 $format — 格式转换
// ---------------------------------------------------------------------------

// @test-case TC3.7-format_timestamp
func TestMapping_FormatConversion(t *testing.T) {
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
}
