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
// for its health endpoint.
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
// Vendor responds with 200 after 3s delay; server should wait for it.
func TestShutdown_WaitDelivery(t *testing.T) {
	t.Run("TC5.1-wait_delivery", func(t *testing.T) {
		projectRoot := getProjectRoot()
		configDir := getTestdataDir("tc5_wait_delivery")

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"sd-wait-vendor"})
		require.NoError(t, err)

		// Configure delayed 200 (3s) -- simulate slow vendor.
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
		defer resp.Body.Close()
		assert.Equal(t, http.StatusAccepted, resp.StatusCode)

		var result apiResponse
		json.NewDecoder(resp.Body).Decode(&result)
		notifID := fmt.Sprint(result.Data["notification_id"])
		require.NotEmpty(t, notifID)

		// Wait for vendor to receive the request (3s delay is in response path)
		require.NotNil(t, suite.MockVendors["sd-wait-vendor"].WaitRequest(10*time.Second),
			"vendor should receive the request")

		// Stop server and measure elapsed -- should wait for the inflight delivery
		start := time.Now()
		suite.StopServer()
		elapsed := time.Since(start)
		require.GreaterOrEqual(t, elapsed, 2*time.Second,
			"server should wait for inflight delivery (~3s vendor delay)")
		require.Less(t, elapsed, 10*time.Second,
			"server should not exceed shutdown timeout by much")

		suite.TearDownSuite()

		// Restart a temporary server to verify delivery completed via API
		serverCmd := startServer(t, configDir, projectRoot, ":8080",
			"postgres://notify:notify@localhost:5432/notification?sslmode=disable",
			"amqp://notify:notify@localhost:5672/",
		)
		defer serverCmd.Process.Kill()

		status, err := waitForStatus("http://localhost:8080", notifID,
			[]string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 15*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status,
			"notification should be SUCCEEDED after graceful shutdown with inflight delivery")

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

// ---------------------------------------------------------------------------
// TC5.2-retry_on_sigterm
// ---------------------------------------------------------------------------

// @test-case TC5.2-retry_on_sigterm
// SIGTERM during first delivery attempt; after restart the retry completes successfully.
func TestShutdown_RetryOnSigterm(t *testing.T) {
	t.Run("TC5.2-retry_on_sigterm", func(t *testing.T) {
		projectRoot := getProjectRoot()
		configDir := getTestdataDir("tc5_retry_on_sigterm")

		suite, err := e2e.SetupSuiteWithConfig(configDir, projectRoot, []string{"sd-retry-vendor"})
		require.NoError(t, err)
		mv := suite.MockVendors["sd-retry-vendor"]

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

		suite.StopServer()
		suite.TearDownSuite()

		// Restart the server with the same config.
		// The retry message is in the retry queue; the new server connects to the
		// same MQ without deleting queues and will consume the retry.
		serverCmd := startServer(t, configDir, projectRoot, ":8080",
			"postgres://notify:notify@localhost:5432/notification?sslmode=disable",
			"amqp://notify:notify@localhost:5672/",
		)
		defer serverCmd.Process.Kill()

		mv2 := e2e.NewMockVendor()
		require.NoError(t, mv2.Start(":19102"))
		defer mv2.Close()

		mv2.RegisterBehavior([]e2e.MockResponse{
			{StatusCode: 200, Body: "ok"},
		})

		// Wait for the retry to complete
		serverURL := "http://localhost:8080"
		status, err := waitForStatus(serverURL, notifID,
			[]string{"SUCCEEDED", "FAILED", "PARTIALLY_FAILED"}, 60*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "SUCCEEDED", status, "notification should eventually be SUCCEEDED after restart")

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
