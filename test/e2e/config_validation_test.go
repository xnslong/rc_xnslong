package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// ---------------------------------------------------------------------------
// TC4.6-undeclared-field — 模板字段引用未声明字段
// ---------------------------------------------------------------------------
//
// Verifies that:
//   - The server starts despite validation errors (不阻塞启动)
//   - The contract is marked unavailable, delivery fails, notification FAILED

// @test-case TC4.6-undeclared-field
func TestConfig_TemplateFieldValidation(t *testing.T) {
	t.Run("TC4.6-undeclared-field", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// Post a notification using the tc43 event type (has invalid contract)
		body := fmt.Sprintf(`{
			"event": "tc43.order.paid",
			"idempotent_key": "%s",
			"payload": {"user_id": "u123", "amount": 5000}
		}`, e2e.NewTestID("TC4.6-undeclared-field"))

		notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)
		require.NotEmpty(t, notifID)

		// The contract is marked unavailable by validateCrossConfig, so delivery
		// cannot proceed. The notification ends up FAILED.
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed}, 15*time.Second)
		require.NoError(t, err)
		require.Equal(t, e2e.StatusFailed, status,
			"notification should be FAILED because the contract references an undeclared field")
	})
}
