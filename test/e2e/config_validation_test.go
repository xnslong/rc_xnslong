package e2e_test

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// ---------------------------------------------------------------------------
// TC4.3-template_field_validation
// ---------------------------------------------------------------------------
//
// Test 4.3: 模板字段引用不存在于 event schema 中的字段
//
// Verifies that:
//   - The server starts despite validation errors (不阻塞启动)
//   - The contract is marked unavailable, delivery fails, notification FAILED

// @test-case TC4.3-template_field_validation
func TestConfig_TemplateFieldValidation(t *testing.T) {
	projectRoot := getProjectRootConfig()

	// Use the common testdata config which includes the tc43 event type
	// with a delivery contract that references an undeclared field.
	configDir := projectRoot + "/test/e2e/testdata/common"

	suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"tpl_valid_vendor"})
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// Post a notification using the tc43 event type (has invalid contract)
	body := `{
		"event": "tc43.order.paid",
		"idempotent_key": "tc43-001",
		"payload": {"user_id": "u123", "amount": 5000}
	}`

	notifID := postAndGetID(t, suite.ServerURL+"/api/v1/notifications", body)
	require.NotEmpty(t, notifID)

	// The contract is marked unavailable by validateCrossConfig, so delivery
	// cannot proceed. The notification ends up FAILED.
	status, err := waitForStatusMapping(suite.ServerURL, notifID, []string{"FAILED"}, 15*time.Second)
	require.NoError(t, err)
	require.Equal(t, "FAILED", status,
		"notification should be FAILED because the contract references an undeclared field")
}

// getProjectRootConfig returns the project root from the test file location.
func getProjectRootConfig() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}
