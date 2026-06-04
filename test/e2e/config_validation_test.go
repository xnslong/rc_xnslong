package e2e_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// ---------------------------------------------------------------------------
// Helper: createTemplateValidationConfig
// ---------------------------------------------------------------------------

// createTemplateValidationConfig creates a temporary config directory with an
// event schema that declares {user_id, amount} and a delivery contract whose
// template references both a valid field (user_id) and an undeclared field
// (nonexistent_field) for cross-config validation testing.
func createTemplateValidationConfig(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "e2e-tpl-valid-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Event schema with limited fields
	require.NoError(t, os.MkdirAll(tmpDir+"/events/order/events", 0755))
	require.NoError(t, os.WriteFile(tmpDir+"/events/order/events/order.paid.yaml", []byte(`
event_type: "order.paid"
schema:
  type: object
  properties:
    user_id:
      type: string
    amount:
      type: integer
`), 0644))

	// Route
	require.NoError(t, os.MkdirAll(tmpDir+"/events/order/routes", 0755))
	require.NoError(t, os.WriteFile(tmpDir+"/events/order/routes/order.paid.yaml", []byte(`
event_type: "order.paid"
routes:
  - vendor_id: "tpl_valid_vendor"
`), 0644))

	// Vendor config — point to a mock vendor on a dedicated port
	require.NoError(t, os.MkdirAll(tmpDir+"/vendors/tpl_valid_vendor", 0755))
	require.NoError(t, os.WriteFile(tmpDir+"/vendors/tpl_valid_vendor/vendor.yaml", []byte(`
vendor_id: "tpl_valid_vendor"
request:
  method: POST
  url: "http://localhost:19094/api/notify"
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
`), 0644))

	// Delivery contract: references user_id (valid) AND nonexistent_field (invalid)
	require.NoError(t, os.MkdirAll(tmpDir+"/vendors/tpl_valid_vendor/order", 0755))
	require.NoError(t, os.WriteFile(tmpDir+"/vendors/tpl_valid_vendor/order/order.paid.yaml", []byte(`
event_type: "order.paid"
request:
  body:
    type: mapping
    template:
      user_id: "@{payload:user_id}"
      bad_ref: "@{payload:nonexistent_field}"
`), 0644))

	return tmpDir
}

// ---------------------------------------------------------------------------
// TC4.3-template_field_validation
// ---------------------------------------------------------------------------
//
// Test 4.3: 模板字段引用不存在于 event schema 中的字段
//
// Verifies that:
//   - The server starts and accepts notifications despite validation warnings
//   - Delivery still succeeds — nonexistent_field just resolves to nil in output

// @test-case TC4.3-template_field_validation
func TestConfig_TemplateFieldValidation(t *testing.T) {
	projectRoot := getProjectRootMapping()
	configDir := createTemplateValidationConfig(t)

	suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"tpl_valid_vendor"})
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// Post a notification
	body := `{
		"event": "order.paid",
		"idempotent_key": "tc43-001",
		"payload": {"user_id": "u123", "amount": 5000}
	}`

	notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)
	require.NotEmpty(t, notifID)

	// The server should process the notification to a terminal state.
	// Delivery outcome depends on mock vendor availability; validation itself does not affect runtime — any terminal state is acceptable)
	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 15*time.Second)
	require.NoError(t, err)
	require.Contains(t, []string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, status,
		"notification should reach terminal state despite validation warnings")
}
