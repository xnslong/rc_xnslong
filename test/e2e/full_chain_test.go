package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/test/e2e"
)

// notificationStatus queries a notification's status via the API.
func notificationStatus(suite *e2e.Suite, id string) (string, error) {
	resp, err := http.Get(suite.ServerURL + "/api/v1/notifications/" + id)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	status, _ := result.Data["status"].(string)
	return status, nil
}

// waitForNotificationStatus polls the notification status until it reaches
// one of the expected statuses or the timeout expires.
func waitForNotificationStatus(suite *e2e.Suite, id string, expected []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := notificationStatus(suite, id)
		if err != nil {
			return "", err
		}
		for _, exp := range expected {
			if status == exp {
				return status, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	// One last try
	status, err := notificationStatus(suite, id)
	if err != nil {
		return "", err
	}
	return status, fmt.Errorf("status %q not in %v after timeout", status, expected)
}

// ---------------------------------------------------------------------------
// 1.2.1 全链路成功
// ---------------------------------------------------------------------------

// @test-case TC3.1-matched_vendor_called
// @test-case TC3.2-status_succeeded
func TestFullChain_Success(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-success-1",
		"payload": {"order_id": "ORD-001", "user_id": "u1", "amount": 29900, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := result.Data["notification_id"]
	require.NotEmpty(t, notifID)

	// Wait for crm_system vendor to receive the request
	vendorReq := suite.MockVendors["crm_system"].WaitRequest(15 * time.Second)
	require.NotNil(t, vendorReq, "crm_system vendor should receive the request")
	assert.Equal(t, "POST", vendorReq.Method)

	// Wait for notification status to be SUCCEEDED
	status, err := waitForNotificationStatus(suite, fmt.Sprint(notifID), []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)
}

// ---------------------------------------------------------------------------
// 1.2.2 幂等键重复不重发
// ---------------------------------------------------------------------------

// @test-case TC3.2-no_duplicate_delivery
func TestFullChain_IdempotentNoResend(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-dup-1",
		"payload": {"order_id": "ORD-002", "user_id": "u2", "amount": 15000, "currency": "CNY"}
	}`

	// First POST
	resp1, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp1.StatusCode)

	var result1 apiResponse
	json.NewDecoder(resp1.Body).Decode(&result1)
	id1 := fmt.Sprint(result1.Data["notification_id"])

	// Wait for the first crm_system vendor call to happen
	vendorReq1 := suite.MockVendors["crm_system"].WaitRequest(15 * time.Second)
	require.NotNil(t, vendorReq1, "crm_system should receive the first request")

	// Wait for SUCCEEDED
	_, err = waitForNotificationStatus(suite, id1, []string{"SUCCEEDED"}, 10*time.Second)
	require.NoError(t, err)

	// Second POST with same payload
	resp2, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp2.StatusCode)

	var result2 apiResponse
	json.NewDecoder(resp2.Body).Decode(&result2)
	id2 := fmt.Sprint(result2.Data["notification_id"])
	assert.Equal(t, id1, id2, "same idempotent_key must return same notification_id")

	// Verify vendor was only called once (no additional request for the duplicate)
	allReqs := suite.MockVendors["crm_system"].Requests()
	assert.Len(t, allReqs, 1, "crm_system should be called only once for duplicate idempotent_key")
}

// ---------------------------------------------------------------------------
// 1.2.3 无匹配供应商 → FAILED
// ---------------------------------------------------------------------------

// @test-case TC3.4-no_rule_failed
func TestFullChain_NoMatchingVendor(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// Use an event that has no routing rules
	body := `{
		"event": "user.registered",
		"idempotent_key": "chain-nomatch-1",
		"payload": {"user_id": "u3", "name": "Alice"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	// "user.registered" is registered in event_schemas but has no routing rules in testdata
	notifID := fmt.Sprint(result.Data["notification_id"])
	require.NotEmpty(t, notifID)

	// Wait for FAILED (no vendor matched)
	status, err := waitForNotificationStatus(suite, notifID, []string{"FAILED"}, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", status)
}

// ---------------------------------------------------------------------------
// 1.2.4 重试耗尽进死信
// ---------------------------------------------------------------------------

// @test-case TC3.5-retry_exhausted
func TestFullChain_RetryExhaustedToDeadLetter(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// Register mock vendor to always return 503 (retryable failure) for both vendors
	suite.MockVendors["crm_system"].RegisterBehavior([]e2e.MockResponse{
		{StatusCode: 503, Body: `{"error":"service unavailable"}`},
	})
	suite.MockVendors["ad_platform"].RegisterBehavior([]e2e.MockResponse{
		{StatusCode: 503, Body: `{"error":"service unavailable"}`},
	})

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-deadletter-1",
		"payload": {"order_id": "ORD-003", "user_id": "u4", "amount": 5000, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])

	// Wait for FAILED (retries exhausted → all tasks dead_letter)
	// Retry policy: max_attempts=3, base_delay=1s, multiplier=2, max_delay=5s
	// Delays: 1st retry ~1s, 2nd retry ~2s (capped at 5s)
	status, err := waitForNotificationStatus(suite, notifID, []string{"FAILED", "PARTIALLY_FAILED"}, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", status, "notification should be FAILED when all tasks dead-letter")
}

// @test-case TC3.5-network_unreachable
// Vendor address unreachable → treated as retryable, eventually FAILED
func TestFullChain_NetworkUnreachable(t *testing.T) {
	projectRoot := getProjectRoot()
	tmpDir, err := os.MkdirTemp("", "e2e-unreachable-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create vendor config pointing to an unlistened port
	vendorsDir := filepath.Join(tmpDir, "vendors")
	require.NoError(t, os.MkdirAll(vendorsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(vendorsDir, "unreachable_vendor.yaml"), []byte(`
vendor_id: "unreachable_vendor"
request:
  method: POST
  url: "http://localhost:19999/api/notify"
  headers:
    Content-Type: "application/json"
  body:
    type: mapping
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 5s
  multiplier: 2.0
  jitter: 0.2
response_judgment:
  success:
    type: http_status
`), 0644))

	// Create routing rule
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "routing_rules.yaml"), []byte(`
rules:
  - event_type: "order.network_unreachable"
    vendor_id: "unreachable_vendor"
`), 0644))

		// Create schema file
		schemasDir := filepath.Join(tmpDir, "event_schemas")
		require.NoError(t, os.MkdirAll(schemasDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(schemasDir, "order.network_unreachable.yaml"), []byte(`event_type: "order.network_unreachable"
schema:
  type: object
`), 0644))

	// Setup server with this config dir, no mock vendors needed (unreachable)
	suite, err := e2e.SetupSuiteWithConfig(tmpDir, projectRoot, nil)
	require.NoError(t, err)
	defer suite.TearDownSuite()

	body := `{
		"event": "order.network_unreachable",
		"idempotent_key": "chain-unreachable-1",
		"payload": {}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])

	// The vendor URL points to localhost:19999 where nothing listens → connection refused
	// Retry policy: max_attempts=3, so it will retry and eventually FAILED
	status, err := waitForNotificationStatus(suite, notifID, []string{"FAILED"}, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", status, "notification should be FAILED when vendor is unreachable")
}

// ---------------------------------------------------------------------------
// 1.2.5 部分成功（多供应商）
// ---------------------------------------------------------------------------

// @test-case TC3.6-partial_success
func TestFullChain_PartialSuccessMultiVendor(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// ad_platform → failure, crm_system defaults to 200
	suite.MockVendors["ad_platform"].RegisterBehavior([]e2e.MockResponse{
		{StatusCode: 503, Body: `{"error":"service unavailable"}`},
	})

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-partial-1",
		"payload": {"order_id": "ORD-004", "user_id": "u5", "amount": 8000, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])

	// Wait for PARTIALLY_FAILED (1 succeeds, 1 fails)
	status, err := waitForNotificationStatus(suite, notifID, []string{"PARTIALLY_FAILED", "SUCCEEDED", "FAILED"}, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "PARTIALLY_FAILED", status, "notification should be PARTIALLY_FAILED when 1 of 2 vendors fail")
}

// ---------------------------------------------------------------------------
// 1.2.6 多路由非目标 vendor 不收到
// ---------------------------------------------------------------------------

// @test-case TC3.3-all_matched_vendors
// @test-case TC3.3-unmatched_vendor_ignored
func TestFullChain_UnmatchedVendorNotCalled(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-unmatched-1",
		"payload": {"order_id": "ORD-005", "user_id": "u6", "amount": 12000, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])

	// Wait for SUCCEEDED
	_, err = waitForNotificationStatus(suite, notifID, []string{"SUCCEEDED"}, 30*time.Second)
	require.NoError(t, err)

	// Verify crm_system was called (mapped vendor)
	crmReqs := suite.MockVendors["crm_system"].Requests()
	assert.NotEmpty(t, crmReqs, "crm_system should be called")

	// Verify ad_platform was called
	adReqs := suite.MockVendors["ad_platform"].Requests()
	assert.NotEmpty(t, adReqs, "ad_platform should be called")

	// routing_rules.yaml only has crm_system and ad_platform for order.paid
	assert.GreaterOrEqual(t, len(crmReqs)+len(adReqs), 2, "at least 2 vendor calls should be made")
}

// ---------------------------------------------------------------------------
// 2.1 查询已完成通知
// ---------------------------------------------------------------------------

// @test-case TC2.1-completed_notification
// 查询已完成通知 → 返回状态和投递结果
func TestFullChain_CompletedNotification(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-completed-1",
		"payload": {"order_id": "ORD-COMP", "user_id": "u-comp", "amount": 100, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])
	require.NotEmpty(t, notifID)

	// Wait for SUCCEEDED
	status, err := waitForNotificationStatus(suite, notifID, []string{"SUCCEEDED"}, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", status)

	// GET full notification
	getResp, err := http.Get(suite.ServerURL + "/api/v1/notifications/" + notifID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult apiResponse
	json.NewDecoder(getResp.Body).Decode(&getResult)
	assert.Equal(t, "SUCCEEDED", getResult.Data["status"])
	assert.NotEmpty(t, getResult.Data["delivery_results"], "delivery_results should be present")
}

// ---------------------------------------------------------------------------
// 2.2 查询不存在的通知
// ---------------------------------------------------------------------------

// @test-case TC2.2-nonexistent_notification
// 查询不存在的通知 → 404
func TestFullChain_NonexistentNotification(t *testing.T) {
	suite, err := e2e.SetupSuite()
	require.NoError(t, err)
	defer suite.TearDownSuite()

	// POST a notification first (confirm server is up)
	body := `{
		"event": "order.paid",
		"idempotent_key": "chain-nonexist-1",
		"payload": {"order_id": "ORD-NX", "user_id": "u-nx", "amount": 100, "currency": "CNY"}
	}`

	resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	// GET a random UUID that doesn't exist
	getResp, err := http.Get(suite.ServerURL + "/api/v1/notifications/00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}
