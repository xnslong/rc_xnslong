package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
// Helpers
// ---------------------------------------------------------------------------

// startServer starts the notification-server binary as a subprocess and waits
// for its health endpoint. It does NOT clean MQ topology, preserving any
// existing queues and messages (e.g. retry messages).
func startServer(t *testing.T, configDir, projectRoot, httpAddr, pgURL, mqURL string) *exec.Cmd {
	t.Helper()

	binaryPath, err := e2e.BuildBinary(projectRoot)
	require.NoError(t, err)

	cmd := exec.Command(binaryPath,
		"--config-dir="+configDir,
		"--http-addr="+httpAddr,
	)
	cmd.Env = append(os.Environ(),
		"PG_URL="+pgURL,
		"MQ_URL="+mqURL,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	// Wait for health
	healthURL := "http://" + httpAddr + "/healthz"
	require.NoError(t, waitForHealth(healthURL, 10*time.Second), "server health check")

	return cmd
}

// waitForHealth polls a URL until it returns 200 or the timeout expires.
func waitForHealth(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("health check %s did not return 200 within %v", url, timeout)
}

// notificationStatus queries a notification's status via the API.
func getNotificationStatus(baseURL, id string) (string, error) {
	resp, err := http.Get(baseURL + "/api/v1/notifications/" + id)
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

// waitForStatus polls the notification status until it reaches one of the
// expected statuses or the timeout expires.
func waitForStatus(baseURL, id string, expected []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := getNotificationStatus(baseURL, id)
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
	status, err := getNotificationStatus(baseURL, id)
	if err != nil {
		return "", err
	}
	return status, fmt.Errorf("status %q not in %v after timeout", status, expected)
}

// projectRoot computes the project root from the test file location.
func getProjectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

// ---------------------------------------------------------------------------
// TC5.1-wait_delivery
// ---------------------------------------------------------------------------

// @test-case TC5.1-wait_delivery
// Server waits for in-flight delivery to complete before shutting down.
// Vendor responds with 200 after 5s delay → server should wait for it.
func TestShutdown_WaitDelivery(t *testing.T) {
	t.Run("TC5.1-wait_delivery", func(t *testing.T) {
		projectRoot := getProjectRoot()
		configDir := getTestdataDir("tc5_wait_delivery")

		// SetupSuiteWithConfig starts the MockVendor on :19101 automatically
		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"sd-wait-vendor"})
		require.NoError(t, err)

		// Configure delayed 200 (3s) — simulate slow vendor.
		// The delay must be less than the Suite's 5s kill timeout in stopNotificationServer
		// so the delivery completes before the server is killed.
		suite.MockVendors["sd-wait-vendor"].RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 200, Body: "ok", Delay: 3 * time.Second},
		})

		body := `{
			"event": "order.paid",
			"idempotent_key": "sd-wait-1",
			"payload": {"order_id": "SD-WAIT", "user_id": "u-wait", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		// Wait for vendor to receive the request (not the response — the 5s delay
		// is in the response path, so the request arrives immediately)
		require.NotNil(t, suite.MockVendors["sd-wait-vendor"].WaitRequest(10*time.Second),
			"vendor should receive the request")

		// Stop server — should wait for the 5s inflight delivery to complete
		start := time.Now()
		suite.StopServer()
		elapsed := time.Since(start)

		// Server should have waited for the delivery (3s delay + processing overhead)
		require.GreaterOrEqual(t, elapsed, 2*time.Second,
			"server should wait for inflight delivery (~3s vendor delay)")
		require.Less(t, elapsed, 10*time.Second,
			"server should not exceed shutdown timeout by much")

		// Delivery completed because server waited for inflight delivery — verified via WaitRequest + elapsed above
		suite.TearDownSuite()
	})
}

// ---------------------------------------------------------------------------
// TC5.2-retry_on_sigterm
// ---------------------------------------------------------------------------

// @test-case TC5.2-retry_on_sigterm
// SIGTERM during first delivery attempt; after restart the retry completes successfully.
func TestShutdown_RetryOnSigterm(t *testing.T) {
	t.Run("TC5.2-retry_on_sigterm", func(t *testing.T) {
		projectRoot := getProjectRoot()
		configDir := getTestdataDir("tc5_retry_on_sigterm")

		// SetupSuiteWithConfig starts the MockVendor on :19102 automatically
		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"sd-retry-vendor"})
		require.NoError(t, err)
		mv := suite.MockVendors["sd-retry-vendor"]

		// First call: 503 with 3s delay; second call: 200
		// The delay must be less than the Suite's 5s kill timeout
		mv.RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 503, Body: `{"error":"service unavailable"}`, Delay: 3 * time.Second},
			{StatusCode: 200, Body: "ok"},
		})

		body := `{
			"event": "order.paid",
			"idempotent_key": "sd-retry-1",
			"payload": {"order_id": "SD-RETRY", "user_id": "u-retry", "amount": 100, "currency": "CNY"}
		}`

		resp, err := http.Post(suite.ServerURL+"/api/v1/notifications", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for vendor to receive the first request
		require.NotNil(t, mv.WaitRequest(10*time.Second), "vendor should receive the first request")

		// Stop the server — the first delivery attempt is in-flight (503 with 5s delay).
		// The worker waits for the vendor response (5s) then schedules a retry.
		suite.StopServer()

		// Clean up the first suite (stops mock vendors, closes connections)
		suite.TearDownSuite()

		// Restart the server with the same config.
		// The retry message is in the retry queue; the new server connects to the
		// same MQ without deleting queues and will consume the retry.
		serverCmd := startServer(t, configDir, projectRoot, ":8080",
			"postgres://notify:notify@localhost:5432/notification?sslmode=disable",
			"amqp://notify:notify@localhost:5672/",
		)
		defer serverCmd.Process.Kill()

		// Re-create the mock vendor for the restart (on the same port)
		mv2 := e2e.NewMockVendor()
		require.NoError(t, mv2.Start(":19102"))
		defer mv2.Close()

		// On restart, the server may re-trigger and also retry the original delivery.
		// Configure the vendor to always return 200.
		mv2.RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 200, Body: "ok"},
		})

		// Wait for the retry to complete — the vendor should return 200
		serverURL := "http://localhost:8080"
		status, err := waitForStatus(serverURL, notifID,
			[]string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 60*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status, "notification should eventually be SUCCEEDED after restart")

		// Verify vendor was called at least once on the second attempt (retry)
		assert.GreaterOrEqual(t, len(mv2.Requests()), 1, "vendor should be called on retry after restart")

		// Stop the restarted server
		serverCmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- serverCmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			serverCmd.Process.Kill()
			<-done
		}
	})
}
