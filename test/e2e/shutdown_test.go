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
// TC5.1-wait_delivery
// ---------------------------------------------------------------------------

// @test-case TC5.1-wait_delivery
// Server waits for in-flight delivery to complete before shutting down.
// Vendor responds with 200 after 3s delay; server should wait for it.
func TestShutdown_WaitDelivery(t *testing.T) {
	t.Run("TC5.1-wait_delivery", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		// Configure delayed 200 (3s) -- simulate slow vendor.
		e2e.Vendor("sd-wait-vendor").RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 200, Body: "ok", Delay: 3 * time.Second},
		})

		body := `{
			"event": "tc5.wait_delivery",
			"idempotent_key": "sd-wait-1",
			"payload": {"order_id": "SD-WAIT", "user_id": "u-wait", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for vendor to receive the request (3s delay is in response path)
		require.NotNil(t, e2e.Vendor("sd-wait-vendor").WaitRequest(10*time.Second),
			"vendor should receive the request")

		// Stop server and measure elapsed -- should wait for the inflight delivery
		start := time.Now()
		e2e.StopServer()
		elapsed := time.Since(start)
		require.GreaterOrEqual(t, elapsed, 2*time.Second,
			"server should wait for inflight delivery (~3s vendor delay)")
		require.Less(t, elapsed, 10*time.Second,
			"server should not exceed shutdown timeout by much")

		// Vendor should have been called exactly once so far (the graceful delivery).
		require.Equal(t, 1, len(e2e.Vendor("sd-wait-vendor").Requests()),
			"vendor should be called exactly once (no retry after 200)")

		// Restart server to verify delivery completed via API
		e2e.StartServer()

		status, err := e2e.WaitForNotificationStatus(notifID,
			[]string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status,
			"notification should be SUCCEEDED after graceful shutdown with inflight delivery")

		// Confirmation of no redelivery: vendor still called exactly once.
		// If the old process had failed to ACK, RabbitMQ would redeliver to the
		// new server and the vendor would receive a second call.
		require.Equal(t, 1, len(e2e.Vendor("sd-wait-vendor").Requests()),
			"vendor should still be called exactly once (no redelivery after restart)")
	})
}

// ---------------------------------------------------------------------------
// TC5.2-retry_on_sigterm
// ---------------------------------------------------------------------------

// @test-case TC5.2-retry_on_sigterm
// SIGTERM during first delivery attempt; after restart the retry completes successfully.
func TestShutdown_RetryOnSigterm(t *testing.T) {
	t.Run("TC5.2-retry_on_sigterm", func(t *testing.T) {
		e2e.Setup()
		defer e2e.TearDown()

		mv := e2e.Vendor("sd-retry-vendor")

		mv.RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 503, Body: `{"error":"service unavailable"}`, Delay: 3 * time.Second},
			{StatusCode: 200, Body: "ok"},
		})

		body := `{
			"event": "tc5.retry_on_sigterm",
			"idempotent_key": "sd-retry-1",
			"payload": {"order_id": "SD-RETRY", "user_id": "u-retry", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for vendor to receive the first request
		require.NotNil(t, mv.WaitRequest(10*time.Second), "vendor should receive the first request")

		// Stop the server while the first delivery is in-flight.
		// The old server may wait for the vendor response (503), attempt a retry
		// via DLX+TTL, and ACK the message. Or it may be cut short, leaving the
		// message unacknowledged in the delivery queue.
		e2e.StopServer()

		// Restart server: it will consume the retry message (from the retry queue
		// if the old server published it, or from the delivery queue if the old
		// server NACK'd or the message was redelivered).
		e2e.StartServer()

		// Wait for the notification to reach a terminal state.
		status, err := e2e.WaitForNotificationStatus(notifID,
			[]string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 60*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status,
			"notification should eventually be SUCCEEDED after restart and retry")

		// At least 2 calls: one from the old server, one from the new server's retry.
		assert.GreaterOrEqual(t, len(mv.Requests()), 2,
			"vendor should be called at least twice (original + retry after restart)")
	})
}
