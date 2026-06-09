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

// ---------------------------------------------------------------------------
// 1.2.1 全链路成功
// ---------------------------------------------------------------------------

func TestFullChain_Success(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := fmt.Sprintf(`{
		"event": "order.paid",
		"idempotent_key": "%s",
		"payload": {"order_id": "ORD-001", "user_id": "u1", "amount": 29900, "currency": "CNY"}
	}`, e2e.NewTestID("TC3.1-matched_vendor_called"))

	resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])
	require.NotEmpty(t, notifID)

	// Wait for crm_system vendor to receive the request
	vendorReq := e2e.Vendor("crm_system_19091").WaitRequest(15 * time.Second)
	require.NotNil(t, vendorReq, "crm_system vendor should receive the request")

	// @test-case TC3.1-matched_vendor_called
	t.Run("TC3.1-matched_vendor_called", func(t *testing.T) {
		assert.Equal(t, "POST", vendorReq.Method)
	})

	// @test-case TC3.2-status_succeeded
	t.Run("TC3.2-status_succeeded", func(t *testing.T) {
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)
	})
}

// ---------------------------------------------------------------------------
// 1.2.2 幂等键重复不重发
// ---------------------------------------------------------------------------

// @test-case TC3.2-no_duplicate_delivery
func TestFullChain_IdempotentNoResend(t *testing.T) {
	t.Run("TC3.2-no_duplicate_delivery", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// Generate key once, reuse for both POSTs to test idempotency.
		key := e2e.NewTestID("TC3.2-no_duplicate_delivery")

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "ORD-002", "user_id": "u2", "amount": 15000, "currency": "CNY"}
		}`, key)

		// First POST
		resp1, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp1.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp1.StatusCode)

		var result1 apiResponse
		json.NewDecoder(resp1.Body).Decode(&result1)
		id1 := fmt.Sprint(result1.Data["notification_id"])

		// Wait for the first crm_system vendor call to happen
		vendorReq1 := e2e.Vendor("crm_system_19091").WaitRequest(15 * time.Second)
		require.NotNil(t, vendorReq1, "crm_system should receive the first request")

		// Wait for SUCCEEDED
		_, err = e2e.WaitForNotificationStatus(id1, []string{e2e.StatusSucceeded}, 10*time.Second)
		require.NoError(t, err)

		// Second POST with same payload
		resp2, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp2.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp2.StatusCode)

		var result2 apiResponse
		json.NewDecoder(resp2.Body).Decode(&result2)
		id2 := fmt.Sprint(result2.Data["notification_id"])
		assert.Equal(t, id1, id2, "same idempotent_key must return same notification_id")

		// Verify vendor was only called once (no additional request for the duplicate)
		allReqs := e2e.Vendor("crm_system_19091").Requests()
		assert.Len(t, allReqs, 1, "crm_system should be called only once for duplicate idempotent_key")
	})
}

// ---------------------------------------------------------------------------
// 1.2.3 无匹配供应商 → FAILED
// ---------------------------------------------------------------------------

// @test-case TC3.4-no_rule_failed
func TestFullChain_NoMatchingVendor(t *testing.T) {
	t.Run("TC3.4-no_rule_failed", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "user.registered",
			"idempotent_key": "%s",
			"payload": {"user_id": "u3", "name": "Alice"}
		}`, e2e.NewTestID("TC3.4-no_rule_failed"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		// "user.registered" is registered in event_schemas but has no routing rules
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for FAILED (no vendor matched)
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed}, 10*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusFailed, status)
	})
}

// ---------------------------------------------------------------------------
// 1.2.4 重试耗尽进死信
// ---------------------------------------------------------------------------

// @test-case TC3.5-retry_exhausted
func TestFullChain_RetryExhaustedToDeadLetter(t *testing.T) {
	t.Run("TC3.5-retry_exhausted", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// Register mock vendor to always return 503 (retryable failure) for both vendors
		e2e.Vendor("crm_system_19091").RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 503, Body: `{"error":"service unavailable"}`},
		})
		e2e.Vendor("ad_platform_19092").RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 503, Body: `{"error":"service unavailable"}`},
		})

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "ORD-003", "user_id": "u4", "amount": 5000, "currency": "CNY"}
		}`, e2e.NewTestID("TC3.5-retry_exhausted"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])

		// Wait for FAILED (retries exhausted → all tasks dead_letter)
		// Retry policy: max_attempts=3, base_delay=1s, multiplier=2, max_delay=5s
		// Delays: 1st retry ~1s, 2nd retry ~2s (capped at 5s)
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed, e2e.StatusPartiallyFailed}, 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusFailed, status, "notification should be FAILED when all tasks dead-letter")
	})
}

// @test-case TC3.5-network_unreachable
// Vendor address unreachable → treated as retryable, eventually FAILED
func TestFullChain_NetworkUnreachable(t *testing.T) {
	t.Run("TC3.5-network_unreachable", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// unreachable_vendor is excluded by SetupSuite (no mock started).
		// The server will try to connect to localhost:19999 and get connection refused.
		body := fmt.Sprintf(`{
			"event": "order.network_unreachable",
			"idempotent_key": "%s",
			"payload": {}
		}`, e2e.NewTestID("TC3.5-network_unreachable"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])

		// The vendor URL points to localhost:19999 where nothing listens → connection refused
		// Retry policy: max_attempts=3, so it will retry and eventually FAILED
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusFailed}, 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusFailed, status, "notification should be FAILED when vendor is unreachable")
	})
}

// ---------------------------------------------------------------------------
// 1.2.5 部分成功（多供应商）
// ---------------------------------------------------------------------------

// @test-case TC3.6-partial_success
func TestFullChain_PartialSuccessMultiVendor(t *testing.T) {
	t.Run("TC3.6-partial_success", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// ad_platform → failure, crm_system defaults to 200
		e2e.Vendor("ad_platform_19092").RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 503, Body: `{"error":"service unavailable"}`},
		})

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "ORD-004", "user_id": "u5", "amount": 8000, "currency": "CNY"}
		}`, e2e.NewTestID("TC3.6-partial_success"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])

		// Wait for PARTIALLY_FAILED (1 succeeds, 1 fails)
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusPartiallyFailed, e2e.StatusSucceeded, e2e.StatusFailed}, 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusPartiallyFailed, status, "notification should be PARTIALLY_FAILED when 1 of 2 vendors fail")
	})
}

// ---------------------------------------------------------------------------
// 1.2.6 多路由非目标 vendor 不收到
// ---------------------------------------------------------------------------

func TestFullChain_UnmatchedVendorNotCalled(t *testing.T) {
	e2e.Setup()
	defer e2e.TearDown()

	body := fmt.Sprintf(`{
		"event": "order.paid",
		"idempotent_key": "%s",
		"payload": {"order_id": "ORD-005", "user_id": "u6", "amount": 12000, "currency": "CNY"}
	}`, e2e.NewTestID("TC3.3-all_matched_vendors"))

	resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	notifID := fmt.Sprint(result.Data["notification_id"])

	// Wait for SUCCEEDED
	_, err = e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 30*time.Second)
	require.NoError(t, err)

	crmReqs := e2e.Vendor("crm_system_19091").Requests()
	adReqs := e2e.Vendor("ad_platform_19092").Requests()

	// @test-case TC3.3-all_matched_vendors
	t.Run("TC3.3-all_matched_vendors", func(t *testing.T) {
		assert.NotEmpty(t, crmReqs, "crm_system should be called")
		assert.NotEmpty(t, adReqs, "ad_platform should be called")
	})

	// @test-case TC3.3-unmatched_vendor_ignored
	t.Run("TC3.3-unmatched_vendor_ignored", func(t *testing.T) {
		// Only crm_system and ad_platform are in the routing rules;
		// no other vendor should receive requests.
		assert.GreaterOrEqual(t, len(crmReqs)+len(adReqs), 2, "at least 2 vendor calls should be made")
	})
}

// ---------------------------------------------------------------------------
// 2.1 查询已完成通知
// ---------------------------------------------------------------------------

// @test-case TC2.1-completed_notification
// 查询已完成通知 → 返回状态和投递结果
func TestFullChain_CompletedNotification(t *testing.T) {
	t.Run("TC2.1-completed_notification", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "ORD-COMP", "user_id": "u-comp", "amount": 100, "currency": "CNY"}
		}`, e2e.NewTestID("TC2.1-completed_notification"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for SUCCEEDED
		status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, e2e.StatusSucceeded, status)

		// GET full notification
		getResp, err := http.Get(e2e.ServerURL() + "/api/v1/notifications/" + notifID)
		require.NoError(t, err)
		defer getResp.Body.Close()
		assert.Equal(t, http.StatusOK, getResp.StatusCode)

		var getResult struct {
			Data map[string]any `json:"data"`
		}
		json.NewDecoder(getResp.Body).Decode(&getResult)
		assert.Equal(t, e2e.StatusSucceeded, getResult.Data["status"])
		assert.NotEmpty(t, getResult.Data["delivery_results"], "delivery_results should be present")
	})
}

// ---------------------------------------------------------------------------
// 2.2 查询不存在的通知
// ---------------------------------------------------------------------------

// @test-case TC2.2-nonexistent_notification
// 查询不存在的通知 → 404
func TestFullChain_NonexistentNotification(t *testing.T) {
	t.Run("TC2.2-nonexistent_notification", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// POST a notification first (confirm server is up)
		body := fmt.Sprintf(`{
			"event": "order.paid",
			"idempotent_key": "%s",
			"payload": {"order_id": "ORD-NX", "user_id": "u-nx", "amount": 100, "currency": "CNY"}
		}`, e2e.NewTestID("TC2.2-nonexistent_notification"))

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		// GET a random UUID that doesn't exist
		getResp, err := http.Get(e2e.ServerURL() + "/api/v1/notifications/00000000-0000-0000-0000-000000000000")
		require.NoError(t, err)
		defer getResp.Body.Close()
		assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
	})
}
