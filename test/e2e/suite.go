package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/xnslong/rc_xnslong/internal/config"
)

const defaultMQURL = "amqp://notify:notify@localhost:5672/"

// ---------------------------------------------------------------------------
// Package-level API
// ---------------------------------------------------------------------------

// GlobalSuite returns the global Suite instance for use in test helpers that
// need to access the suite object directly. Most tests should use the
// package-level functions (Setup, Vendor, ServerURL, etc.).
func GlobalSuite() *Suite {
	if global == nil {
		panic("e2e.SetupSuite() must be called before accessing the global suite")
	}
	return global
}

var global *Suite

// Option configures SetupSuite behavior.
type Option func(*suiteConfig)

type suiteConfig struct {
	excludeVendors []string
}

// ExcludeVendors returns an Option that prevents the specified vendors from
// having mock servers started during SetupSuite. Excluded vendors can still
// be started per-test with StartVendor().
func ExcludeVendors(ids ...string) Option {
	return func(cfg *suiteConfig) {
		cfg.excludeVendors = append(cfg.excludeVendors, ids...)
	}
}

// SetupSuite initializes the global test suite. Called once from TestMain.
// It loads config, starts mock vendors (except those in ExcludeVendors), and
// records the initial state for TearDown() to restore to.
func SetupSuite(opts ...Option) {
	cfg := &suiteConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	_, filename, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(filename), "testdata")
	projRoot := filepath.Join(filepath.Dir(filename), "..", "..")

	excluded := make(map[string]bool)
	for _, id := range cfg.excludeVendors {
		excluded[id] = true
	}

	loader, err := config.NewLoader(testdataDir)
	if err != nil {
		log.Fatalf("e2e SetupSuite: create config loader: %v", err)
	}
	if err := loader.Load(context.Background()); err != nil {
		log.Fatalf("e2e SetupSuite: load config: %v", err)
	}

	s := &Suite{
		config:         loader,
		projectRoot:    projRoot,
		configDir:      testdataDir,
		mockVendors:    make(map[string]*MockVendor),
		excluded:       excluded,
	}

	// Record the initial vendor set (excluded vendors are not in initialRunning)
	for _, vendorID := range loader.GetAllVendorIDs() {
		if !excluded[vendorID] {
			s.initialRunningVendors = append(s.initialRunningVendors, vendorID)
		}
	}
	s.initialRunningVendorsSnapshot = make([]string, len(s.initialRunningVendors))
	copy(s.initialRunningVendorsSnapshot, s.initialRunningVendors)

	// Start mock vendors for non-excluded vendors
	for _, vendorID := range s.initialRunningVendors {
		if err := s.startVendorMock(vendorID); err != nil {
			log.Fatalf("e2e SetupSuite: start mock vendor %s: %v", vendorID, err)
		}
	}

	global = s
	log.Printf("e2e SetupSuite: %d vendors started (%d excluded)",
		len(s.initialRunningVendors), len(excluded))
}

// TearDownSuite stops all resources. Called once from TestMain.
// Must be called to release mock vendors and server processes.
func TearDownSuite() {
	s := global
	if s == nil {
		return
	}
	s.stopServer()
	for _, id := range s.initialRunningVendorsSnapshot {
		s.stopVendorMock(id)
	}
	// Also stop any extra vendors that may have been started per-test
	for id := range s.mockVendors {
		s.stopVendorMock(id)
	}
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
	global = nil
}

// Setup prepares the test case environment.
// It restores infrastructure (server, vendors) to the initial state, then
// resets vendor request history and behavior. This guarantees a clean,
// predictable starting point regardless of what the previous test did.
func Setup() {
	s := global
	if s == nil {
		panic("e2e.SetupSuite() must be called before e2e.Setup()")
	}
	s.restoreServer()
	s.restoreVendors()
	s.resetVendors()
}

// TearDown cleans up the current test case's traces.
// It resets vendor state (request history, custom behavior) so the next
// test starts clean. It does NOT restore infrastructure — that is the
// responsibility of Setup.
func TearDown() {
	s := global
	if s == nil {
		return
	}
	s.resetVendors()
}

// ServerURL returns the HTTP base URL of the shared notification server.
func ServerURL() string {
	s := global
	if s == nil || s.serverURL == "" {
		return "http://localhost:8080"
	}
	return s.serverURL
}

// Vendor returns the mock for the given vendor ID, or nil if the vendor
// mock is not running. Use StartVendor/StopVendor to control lifecycle.
func Vendor(id string) *MockVendor {
	s := global
	if s == nil {
		return nil
	}
	return s.mockVendors[id]
}

// StartVendor starts a mock server for the given vendor. No-op if already running.
func StartVendor(id string) {
	s := global
	if s == nil {
		return
	}
	if _, ok := s.mockVendors[id]; ok {
		return // already running
	}
	if err := s.startVendorMock(id); err != nil {
		log.Printf("e2e StartVendor(%s): %v", id, err)
	}
}

// StopVendor stops the mock server for the given vendor. No-op if not running.
func StopVendor(id string) {
	s := global
	if s == nil {
		return
	}
	s.stopVendorMock(id)
}

// StartServer starts the shared notification server. No-op if already running.
func StartServer() {
	s := global
	if s == nil {
		return
	}
	if s.notifCmd != nil && !s.serverStopped {
		return
	}
	s.startServerProcess()
}

// StopServer sends SIGTERM to the shared notification server and waits for
// it to exit (up to 5s, then kills).
func StopServer() {
	s := global
	if s == nil {
		return
	}
	s.stopServer()
}

// WaitForNotificationStatus polls the notification status via HTTP API until
// it reaches one of the expected statuses or the timeout expires.
func WaitForNotificationStatus(notifID string, expected []string, timeout time.Duration) (string, error) {
	baseURL := ServerURL()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v1/notifications/" + notifID)
		if err != nil {
			return "", err
		}
		var result struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()
		for _, exp := range expected {
			if result.Data.Status == exp {
				return result.Data.Status, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	// One last try
	resp, err := http.Get(baseURL + "/api/v1/notifications/" + notifID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Data.Status, fmt.Errorf("status %q not in %v after timeout", result.Data.Status, expected)
}

// GetConfigDir returns the absolute path to the testdata config directory.
func GetConfigDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata")
}

// GetProjectRoot returns the absolute path to the project root.
func GetProjectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

// ---------------------------------------------------------------------------
// Suite — internal struct
// ---------------------------------------------------------------------------

// Suite holds the shared E2E test infrastructure.
type Suite struct {
	serverURL string
	notifCmd  *exec.Cmd

	mockVendors         map[string]*MockVendor
	mockVendorMu        sync.Mutex
	config              *config.Loader
	projectRoot         string
	configDir           string

	// State tracking for TearDown restoration
	excluded                    map[string]bool
	initialRunningVendors       []string // populated during SetupSuite
	initialRunningVendorsSnapshot []string // immutable copy of initial state
	serverStopped               bool
	cleanups                    []func()
}

// ---------------------------------------------------------------------------
// Server lifecycle
// ---------------------------------------------------------------------------

func (s *Suite) ensureServerRunning() {
	if s.serverStopped || s.notifCmd == nil {
		s.startServerProcess()
	}
}

func (s *Suite) startServerProcess() {
	if s.notifCmd != nil && !s.serverStopped {
		return
	}

	binaryPath, err := BuildBinary(s.projectRoot)
	if err != nil {
		log.Fatalf("e2e: build notification-server: %v", err)
	}

	mqURL := os.Getenv("MQ_URL")
	if mqURL == "" {
		mqURL = defaultMQURL
	}

	s.serverURL = "http://localhost:8080"
	s.notifCmd = exec.Command(binaryPath,
		"--config-dir="+s.configDir,
		"--http-addr=:8080",
	)
	s.notifCmd.Env = append(os.Environ(), "MQ_URL="+mqURL)
	s.notifCmd.Dir = s.projectRoot
	s.notifCmd.Stdout = os.Stdout
	s.notifCmd.Stderr = os.Stderr

	if err := s.notifCmd.Start(); err != nil {
		log.Fatalf("e2e: start notification-server: %v", err)
	}

	if err := waitForHealth(s.serverURL+"/healthz", 10*time.Second); err != nil {
		log.Fatalf("e2e: notification-server health check: %v", err)
	}

	s.serverStopped = false
	log.Printf("e2e: notification-server started (pid %d)", s.notifCmd.Process.Pid)
}

func (s *Suite) stopServer() {
	if s.notifCmd == nil || s.notifCmd.Process == nil {
		return
	}

	log.Printf("e2e: sending SIGTERM to notification-server (pid %d)", s.notifCmd.Process.Pid)
	s.notifCmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		done <- s.notifCmd.Wait()
	}()

	select {
	case <-done:
		log.Printf("e2e: notification-server stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Printf("e2e: notification-server did not stop in time, killing")
		s.notifCmd.Process.Kill()
		<-done
	}

	s.notifCmd = nil
	s.serverStopped = true
}

// restoreServer restarts the shared server if it was stopped by a previous test.
// This is called by Setup before each test, not by TearDown.
func (s *Suite) restoreServer() {
	if s.notifCmd == nil || s.serverStopped {
		s.startServerProcess()
	}
}

// ---------------------------------------------------------------------------
// Mock vendor lifecycle
// ---------------------------------------------------------------------------

func (s *Suite) startVendorMock(vendorID string) error {
	s.mockVendorMu.Lock()
	defer s.mockVendorMu.Unlock()

	if _, ok := s.mockVendors[vendorID]; ok {
		return nil // already running
	}

	vendorCfg, err := s.config.GetVendorConfig(vendorID)
	if err != nil {
		return fmt.Errorf("vendor config not found: %w", err)
	}

	u, err := url.Parse(vendorCfg.BaseURL)
	if err != nil {
		return fmt.Errorf("parse vendor base URL %q: %w", vendorCfg.BaseURL, err)
	}

	mv := NewMockVendor()
	if err := mv.Start(":" + u.Port()); err != nil {
		return fmt.Errorf("start mock vendor %s on port %s: %w", vendorID, u.Port(), err)
	}
	s.mockVendors[vendorID] = mv
	log.Printf("e2e: mock vendor %s started on port %s", vendorID, u.Port())
	return nil
}

func (s *Suite) stopVendorMock(vendorID string) {
	s.mockVendorMu.Lock()
	defer s.mockVendorMu.Unlock()

	mv, ok := s.mockVendors[vendorID]
	if !ok {
		return
	}
	mv.Close()
	delete(s.mockVendors, vendorID)
}

func (s *Suite) resetVendors() {
	s.mockVendorMu.Lock()
	defer s.mockVendorMu.Unlock()

	for _, mv := range s.mockVendors {
		mv.Reset()
	}
}

// restoreVendors reconciles the running vendor set against the initial snapshot.
// Vendors that were in the initial set but have been stopped are restarted;
// vendors that were started outside the initial set are stopped.
// This is called by Setup before each test to ensure a predictable environment.
func (s *Suite) restoreVendors() {
	s.mockVendorMu.Lock()
	defer s.mockVendorMu.Unlock()

	running := make(map[string]bool)
	for id := range s.mockVendors {
		running[id] = true
	}

	// Restart vendors that were in the initial set but are no longer running
	for _, id := range s.initialRunningVendorsSnapshot {
		if !running[id] {
			// Release the lock while starting (it may block on port binding)
			s.mockVendorMu.Unlock()
			err := s.startVendorMock(id)
			s.mockVendorMu.Lock()
			if err != nil {
				log.Printf("e2e restoreVendors: failed to restart vendor %s: %v", id, err)
			}
		}
	}

	// Stop vendors that are running but not in the initial set
	for id := range running {
		isInitial := false
		for _, initialID := range s.initialRunningVendorsSnapshot {
			if id == initialID {
				isInitial = true
				break
			}
		}
		if !isInitial {
			mv := s.mockVendors[id]
			mv.Close()
			delete(s.mockVendors, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

// BuildBinary builds the notification-server binary via build.sh and returns its path.
func BuildBinary(projectRoot string) (string, error) {
	binaryPath := filepath.Join(projectRoot, "output", "notification-server")

	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}

	buildScript := filepath.Join(projectRoot, "build.sh")
	cmd := exec.Command("/bin/bash", buildScript)
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build.sh failed: %w", err)
	}

	if _, err := os.Stat(binaryPath); err != nil {
		return "", fmt.Errorf("binary not found at %s after running build.sh: %w", binaryPath, err)
	}
	return binaryPath, nil
}
