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

// Notification statuses, mirrored from internal/model for use in test assertions.
const (
	StatusPending         = "PENDING"
	StatusDelivering      = "DELIVERING"
	StatusSucceeded       = "SUCCEEDED"
	StatusFailed          = "FAILED"
	StatusPartiallyFailed = "PARTIALLY_FAILED"
)

// Delivery task statuses.
const (
	TaskStatusDelivering = "DELIVERING"
	TaskStatusSucceeded  = "SUCCEEDED"
	TaskStatusDeadLetter = "DEAD_LETTER"
)

// NewTestID generates a unique idempotent key using the test case ID as prefix.
// Call from any test: `idempotent_key: e2e.NewTestID("TC3.1-matched_vendor_called")`
// The nanosecond suffix ensures uniqueness across runs, so repeated `go test`
// calls create fresh notifications rather than hitting idempotency.
func NewTestID(tcID string) string {
	return fmt.Sprintf("%s-%d", tcID, time.Now().UnixNano())
}

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

// VendorSpec declares a vendor mock to be started by SetupSuite with a
// specific port. Used with IncludeVendors to supplement vendors whose
// configuration is intentionally broken (e.g. unparseable vendor.yaml) so
// the test framework can start their mocks without reading vendor.yaml.
type VendorSpec struct {
	ID   string
	Port int
}

// Option configures SetupSuite behavior.
type Option func(*suiteConfig)

type suiteConfig struct {
	includeVendors []VendorSpec
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

// IncludeVendors returns an Option that explicitly declares which vendors
// need mock servers started during SetupSuite, along with their ports.
//
// This is needed when the config directory contains vendors with invalid
// configuration (e.g. unparseable vendor.yaml) that prevents the config
// scanner from cleanly discovering their port. In that case, IncludeVendors
// lets the test declare the port that both the vendor.yaml and the test
// agreed upon — the test framework starts the mock on that port regardless
// of whether the system can parse the config.
func IncludeVendors(specs ...VendorSpec) Option {
	return func(cfg *suiteConfig) {
		cfg.includeVendors = append(cfg.includeVendors, specs...)
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

	// Build port overrides from IncludeVendors.
	// These take priority over vendor.yaml's base_url port so that vendors
	// with broken config can still have mocks started.
	portOverrides := make(map[string]int)
	for _, spec := range cfg.includeVendors {
		portOverrides[spec.ID] = spec.Port
	}

	s := &Suite{
		config:        loader,
		projectRoot:   projRoot,
		configDir:     testdataDir,
		mockVendors:   make(map[string]*MockVendor),
		excluded:      excluded,
		portOverrides: portOverrides,
	}

	// Record the initial vendor set — discovered via config scanning, plus
	// any explicitly declared via IncludeVendors.
	// Vendors with corrupt vendor.yaml still appear in GetAllVendorIDs()
	// (the loader creates an error entry). If such a vendor also has a port
	// override via IncludeVendors, we still start its mock — see
	// startVendorMock for the fallback logic.
	var initialVendors []string
	for _, vendorID := range loader.GetAllVendorIDs() {
		if excluded[vendorID] {
			continue
		}
		// If vendor config is loadable, start the mock normally.
		// If not, only start if a port override exists (from IncludeVendors).
		if _, err := loader.GetVendorConfig(vendorID); err != nil {
			if _, ok := portOverrides[vendorID]; !ok {
				log.Printf("e2e SetupSuite: skipping mock for vendor %s (config error: %v, no port override)", vendorID, err)
				continue
			}
			log.Printf("e2e SetupSuite: starting vendor %s with port override (config error: %v)", vendorID, err)
		}
		initialVendors = append(initialVendors, vendorID)
	}
	for _, spec := range cfg.includeVendors {
		if !excluded[spec.ID] {
			// Add if not already in the set from config scanning.
			found := false
			for _, id := range initialVendors {
				if id == spec.ID {
					found = true
					break
				}
			}
			if !found {
				initialVendors = append(initialVendors, spec.ID)
			}
		}
	}
	s.initialRunningVendors = initialVendors
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

	mockVendors  map[string]*MockVendor
	mockVendorMu sync.Mutex
	config       *config.Loader
	projectRoot  string
	configDir    string

	// State tracking for TearDown restoration
	excluded                      map[string]bool
	portOverrides                 map[string]int // port by vendor ID, populated from IncludeVendors
	initialRunningVendors         []string       // populated during SetupSuite
	initialRunningVendorsSnapshot []string       // immutable copy of initial state
	serverStopped                 bool
	cleanups                      []func()
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

	binaryPath, err := BinaryPath(s.projectRoot)
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

// startVendorMock starts a mock server for the given vendor.
//
// Port resolution priority:
//  1. If the vendor ID has a port override (from IncludeVendors), use it.
//     This covers vendors with broken vendor.yaml that still need a mock —
//     the test declares the port explicitly, independent of whether the
//     system can parse their config.
//  2. Otherwise, read port from vendor.yaml's base_url.
//
// Priority 1 exists because a vendor's API server is always running
// regardless of the notification system's config state. The test framework
// must be able to start a mock even when the system cannot parse the config
// — only then can we distinguish "system correctly skipped the vendor" from
// "system had a bug but we couldn't detect it because no mock was listening."
func (s *Suite) startVendorMock(vendorID string) error {
	s.mockVendorMu.Lock()
	defer s.mockVendorMu.Unlock()

	if _, ok := s.mockVendors[vendorID]; ok {
		return nil // already running
	}

	var port string

	// Priority 1: check for port override from IncludeVendors
	if overridePort, ok := s.portOverrides[vendorID]; ok {
		port = fmt.Sprintf("%d", overridePort)
	}

	// Priority 2: read from vendor.yaml's base_url
	if port == "" {
		vendorCfg, err := s.config.GetVendorConfig(vendorID)
		if err != nil {
			return fmt.Errorf("vendor %s: config error (%w) and no port override from IncludeVendors", vendorID, err)
		}
		u, err := url.Parse(vendorCfg.BaseURL)
		if err != nil {
			return fmt.Errorf("vendor %s: parse base URL %q: %w", vendorID, vendorCfg.BaseURL, err)
		}
		port = u.Port()
	}

	mv := NewMockVendor()
	if err := mv.Start(":" + port); err != nil {
		return fmt.Errorf("start mock vendor %s on port %s: %w", vendorID, port, err)
	}
	s.mockVendors[vendorID] = mv
	log.Printf("e2e: mock vendor %s started on port %s", vendorID, port)
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

// BinaryPath returns the path to the notification-server binary.
// The binary must already exist — test_e2e.sh builds it before running tests.
func BinaryPath(projectRoot string) (string, error) {
	binaryPath := filepath.Join(projectRoot, "output", "notification-server")

	if _, err := os.Stat(binaryPath); err != nil {
		return "", fmt.Errorf("binary not found at %s (run build.sh or test_e2e.sh first): %w", binaryPath, err)
	}
	return binaryPath, nil
}
