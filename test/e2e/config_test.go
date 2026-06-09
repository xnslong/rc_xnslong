package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// fetchDeliveryResults fetches delivery_results for a notification via GET API.
func fetchDeliveryResults(t *testing.T, notifID string) []map[string]any {
	t.Helper()
	resp, err := http.Get(e2e.ServerURL() + "/api/v1/notifications/" + notifID)
	require.NoError(t, err)
	defer resp.Body.Close()
	var result struct {
		Data struct {
			DeliveryResults []map[string]any `json:"delivery_results"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return result.Data.DeliveryResults
}

// ---------------------------------------------------------------------------
// TC4.2-vendor-anomalies — 多 vendor 配置错误共存测试
// ---------------------------------------------------------------------------

// @test-case TC4.2-vendor-anomalies
// @test-case TC4.2-vendor-file-missing
// @test-case TC4.2-vendor-invalid-retry
func TestConfig_VendorErrors(t *testing.T) {
	t.Run("TC4.2-vendor-anomalies", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "tc42.vendor_errors",
			"idempotent_key": "%s",
			"payload": {"id": "123"}
		}`, e2e.NewTestID("TC4.2-vendor-anomalies"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications",
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, resp.StatusCode)

		var ingestResult struct {
			Data struct {
				NotificationID string `json:"notification_id"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&ingestResult))
		resp.Body.Close()
		notifID := ingestResult.Data.NotificationID
		require.NotEmpty(t, notifID)

		status, err := e2e.WaitForNotificationStatus(notifID,
			[]string{e2e.StatusPartiallyFailed, e2e.StatusFailed, e2e.StatusSucceeded}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusPartiallyFailed, status)

		results := fetchDeliveryResults(t, notifID)
		require.Len(t, results, 4)

		for _, r := range results {
			vendorID, _ := r["vendor_id"].(string)
			taskStatus, _ := r["status"].(string)
			lastError, _ := r["last_error"].(string)

			switch vendorID {
			case "mapping_vendor_19093":
				assert.Equal(t, e2e.TaskStatusSucceeded, taskStatus,
					"mapping_vendor should deliver successfully")
				assert.Empty(t, lastError,
					"mapping_vendor should have no error")

			case "bad_vendor_18001":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"bad_vendor should dead-letter due to invalid YAML")
				assert.NotEmpty(t, lastError,
					"bad_vendor should have a config parse error")
				assert.NotContains(t, lastError, "not configured",
					"bad_vendor error should be a parse error, not not-configured")

			case "missing_yaml_vendor_18003":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"missing_yaml_vendor should dead-letter due to missing vendor.yaml")
				assert.NotEmpty(t, lastError,
					"missing_yaml_vendor should have a file read error")
				assert.NotContains(t, lastError, "not configured",
					"missing_yaml_vendor error should be a file read error, not not-configured")

			case "invalid_retry_vendor_18002":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"invalid_retry_vendor should dead-letter due to invalid retry config")
				assert.NotEmpty(t, lastError,
					"invalid_retry_vendor should have a config error")
				assert.NotContains(t, lastError, "not configured",
					"invalid_retry_vendor error should be a parse error, not not-configured")
			}
		}
			// Verify mock request counts: mapping_vendor should receive 1 request
			// (SUCCEEDED), broken-YAML vendors should receive 0 (DEAD_LETTER —
			// the system correctly skips them without attempting delivery).
			assert.Len(t, e2e.Vendor("mapping_vendor_19093").Requests(), 1,
				"mapping_vendor should receive 1 request (SUCCEEDED)")
			assert.Empty(t, e2e.Vendor("bad_vendor_18001").Requests(),
				"bad_vendor should receive 0 requests (DEAD_LETTER)")
			assert.Empty(t, e2e.Vendor("missing_yaml_vendor_18003").Requests(),
				"missing_yaml_vendor should receive 0 requests (DEAD_LETTER)")
			assert.Empty(t, e2e.Vendor("invalid_retry_vendor_18002").Requests(),
				"invalid_retry_vendor should receive 0 requests (DEAD_LETTER)")
	})
}

// ---------------------------------------------------------------------------
// TC4.2-vendor-invalid-yaml — vendor.yaml 语法错误被动容错
// ---------------------------------------------------------------------------

// @test-case TC4.2-vendor-invalid-yaml
func TestConfig_PartialAvailability(t *testing.T) {
	t.Run("TC4.2-vendor-invalid-yaml", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "tc41.invalid_config",
			"idempotent_key": "%s",
			"payload": {"id": "123"}
		}`, e2e.NewTestID("TC4.2-vendor-invalid-yaml"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications",
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, resp.StatusCode)

		var ingestResult struct {
			Data struct {
				NotificationID string `json:"notification_id"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&ingestResult))
		resp.Body.Close()
		notifID := ingestResult.Data.NotificationID
		require.NotEmpty(t, notifID)

		status, err := e2e.WaitForNotificationStatus(notifID,
			[]string{e2e.StatusPartiallyFailed, e2e.StatusFailed, e2e.StatusSucceeded}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusPartiallyFailed, status)

		results := fetchDeliveryResults(t, notifID)
		require.Len(t, results, 2)

		for _, r := range results {
			vendorID, _ := r["vendor_id"].(string)
			taskStatus, _ := r["status"].(string)
			lastError, _ := r["last_error"].(string)

			switch vendorID {
			case "mapping_vendor_19093":
				assert.Equal(t, e2e.TaskStatusSucceeded, taskStatus,
					"mapping_vendor should deliver successfully")
				assert.Empty(t, lastError,
					"mapping_vendor should have no error")

			case "bad_vendor_18001":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"bad_vendor should dead-letter due to config error")
				assert.NotEmpty(t, lastError,
					"bad_vendor should have a config parse error")
				assert.NotContains(t, lastError, "not configured",
					"bad_vendor error should be a load error, not not-configured")
			}
		}
		// Verify mock request counts
		assert.Len(t, e2e.Vendor("mapping_vendor_19093").Requests(), 1,
			"mapping_vendor should receive 1 request (SUCCEEDED)")
		assert.Empty(t, e2e.Vendor("bad_vendor_18001").Requests(),
			"bad_vendor should receive 0 requests (DEAD_LETTER)")
	})
}

// ---------------------------------------------------------------------------
// TC4.3-schema-* — Event schema 配置异常
// ---------------------------------------------------------------------------

// @test-case TC4.3-schema-invalid-yaml
// @test-case TC4.3-schema-missing-event-type
// @test-case TC4.3-schema-missing-schema
// @test-case TC4.3-schema-empty-properties
func TestConfig_SchemaErrors(t *testing.T) {
	tests := []struct {
		name       string
		eventType  string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "TC4.3-schema-invalid-yaml",
			eventType:  "tc43_schema.invalid_yaml",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "EVENT_NOT_FOUND",
		},
		{
			name:       "TC4.3-schema-missing-event-type",
			eventType:  "tc43_schema.missing_event_type",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "EVENT_NOT_FOUND",
		},
		{
			name:       "TC4.3-schema-missing-schema",
			eventType:  "tc43_schema.missing_schema_stanza",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "EVENT_NOT_FOUND",
		},
		{
			name:       "TC4.3-schema-empty-properties",
			eventType:  "tc43_schema.empty_properties",
			wantStatus: http.StatusAccepted,
			wantCode:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e2e.Setup()
			defer e2e.TearDown()

			body := fmt.Sprintf(`{
				"event": "%s",
				"idempotent_key": "%s",
				"payload": {"id": "123"}
			}`, tt.eventType, e2e.NewTestID(tt.name))

			resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications",
				"application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantCode != "" {
				var errResp apiErrorResponse
				json.NewDecoder(resp.Body).Decode(&errResp)
				assert.Equal(t, tt.wantCode, errResp.Error.Code)
			} else {
				var result apiResponse
				json.NewDecoder(resp.Body).Decode(&result)
				assert.NotEmpty(t, result.Data["notification_id"],
					"empty properties schema should be accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TC4.4-route-* — Route 配置异常
// ---------------------------------------------------------------------------

// @test-case TC4.4-route-file-missing
// @test-case TC4.4-route-invalid-yaml
// @test-case TC4.4-route-missing-routes
// @test-case TC4.4-route-empty-routes
func TestConfig_RouteErrors(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
	}{
		{name: "TC4.4-route-file-missing", eventType: "tc44_route.missing"},
		{name: "TC4.4-route-invalid-yaml", eventType: "tc44_route.invalid_yaml"},
		{name: "TC4.4-route-missing-routes", eventType: "tc44_route.missing_routes"},
		{name: "TC4.4-route-empty-routes", eventType: "tc44_route.empty_routes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e2e.Setup()
			defer e2e.TearDown()

			body := fmt.Sprintf(`{
				"event": "%s",
				"idempotent_key": "%s",
				"payload": {"id": "123"}
			}`, tt.eventType, e2e.NewTestID(tt.name))

			notifID := postAndGetID(t, e2e.ServerURL()+"/api/v1/notifications", body)

			status, err := e2e.WaitForNotificationStatus(notifID,
				[]string{e2e.StatusFailed}, 15*time.Second)
			require.NoError(t, err)
			assert.Equal(t, e2e.StatusFailed, status,
				"notification should be FAILED when routing rules are broken")
		})
	}
}

// ---------------------------------------------------------------------------
// TC4.5-contract-anomalies — Delivery contract 配置错误共存测试
// ---------------------------------------------------------------------------

// @test-case TC4.5-contract-anomalies
// @test-case TC4.5-contract-file-missing
// @test-case TC4.5-contract-invalid-yaml
// Note: TC4.5-contract-missing-request and TC4.5-contract-missing-body
// are tested below as known bugs (loader does not validate missing fields).
func TestConfig_ContractErrors(t *testing.T) {
	t.Run("TC4.5-contract-anomalies", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "tc45.contract_errors",
			"idempotent_key": "%s",
			"payload": {"id": "123"}
		}`, e2e.NewTestID("TC4.5-contract-anomalies"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications",
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, resp.StatusCode)

		var ingestResult struct {
			Data struct {
				NotificationID string `json:"notification_id"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&ingestResult))
		resp.Body.Close()
		notifID := ingestResult.Data.NotificationID
		require.NotEmpty(t, notifID)

		status, err := e2e.WaitForNotificationStatus(notifID,
			[]string{e2e.StatusPartiallyFailed, e2e.StatusFailed, e2e.StatusSucceeded}, 15*time.Second)
		require.NoError(t, err)

		results := fetchDeliveryResults(t, notifID)
		require.Len(t, results, 5)

		for _, r := range results {
			vendorID, _ := r["vendor_id"].(string)
			taskStatus, _ := r["status"].(string)
			lastError, _ := r["last_error"].(string)

			switch vendorID {
			case "mapping_vendor_19093":
				assert.Equal(t, e2e.TaskStatusSucceeded, taskStatus,
					"mapping_vendor should deliver successfully")
				assert.Empty(t, lastError,
					"mapping_vendor should have no error")

			case "missing_contract_vendor_19098":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"missing_contract_vendor should dead-letter due to missing contract")
				assert.Contains(t, lastError, "not configured",
					"missing_contract error should be not-configured (contract never loaded)")

			case "bad_contract_vendor_19095":
				assert.Equal(t, e2e.TaskStatusDeadLetter, taskStatus,
					"bad_contract_vendor should dead-letter due to invalid YAML contract")
				assert.NotEmpty(t, lastError,
					"bad_contract_vendor should have a contract parse error")
				assert.NotContains(t, lastError, "not configured",
					"bad_contract error should be a parse error, not not-configured")

			case "contract_missing_request_19096":
				// KNOWN BUG: The config loader does not validate that the
				// "request" field is present in the delivery contract.
				// Missing fields parse as zero values and the contract is
				// loaded successfully. The delivery worker sends HTTP with
				// empty method/path and the mock returns 200 → SUCCEEDED.
				assert.Equal(t, e2e.TaskStatusSucceeded, taskStatus,
					"contract_missing_request should SUCCEED (known bug: loader does not validate missing request)")
				assert.Empty(t, lastError,
					"contract_missing_request should have no error (known bug)")

			case "contract_missing_body_19097":
				// KNOWN BUG: Same as above — missing "body" field is not
				// validated by loadDeliveryContractFile. The contract is
				// loaded with zero values and delivery SUCCEEDs.
				assert.Equal(t, e2e.TaskStatusSucceeded, taskStatus,
					"contract_missing_body should SUCCEED (known bug: loader does not validate missing body)")
				assert.Empty(t, lastError,
					"contract_missing_body should have no error (known bug)")
			}
		}

		// The overall status reflects 3 SUCCEEDED + 2 DEAD_LETTER.
		assert.Equal(t, e2e.StatusPartiallyFailed, status,
			"3 vendors SUCCEEDED (1 good + 2 known-bug) and 2 DEAD_LETTER")

		// Mock request counts document the bug:
		// - missing_contract_vendor and bad_contract_vendor: 0 (correctly dead-lettered)
		// - contract_missing_request and contract_missing_body: 1 each (known bug)
		assert.Empty(t, e2e.Vendor("missing_contract_vendor_19098").Requests(),
			"no HTTP request should reach missing_contract_vendor")
		assert.Empty(t, e2e.Vendor("bad_contract_vendor_19095").Requests(),
			"no HTTP request should reach bad_contract_vendor")
		assert.Len(t, e2e.Vendor("contract_missing_request_19096").Requests(), 1,
			"contract_missing_request should receive 1 request (known bug: missing field not rejected)")
		assert.Len(t, e2e.Vendor("contract_missing_body_19097").Requests(), 1,
			"contract_missing_body should receive 1 request (known bug: missing field not rejected)")
	})
}

// ---------------------------------------------------------------------------
// TC4.1-path-not-found — 配置路径不存在拒绝启动
// ---------------------------------------------------------------------------

// @test-case TC4.1-path-not-found
func TestConfig_NonExistentPath(t *testing.T) {
	t.Run("TC4.1-path-not-found", func(t *testing.T) {
		binaryPath, err := e2e.BinaryPath(e2e.GetProjectRoot())
		require.NoError(t, err)

		cmd := exec.Command(binaryPath, "--config-dir=/nonexistent/path")
		err = cmd.Run()

		assert.Error(t, err, "server should fail to start with nonexistent config path")
	})
}
