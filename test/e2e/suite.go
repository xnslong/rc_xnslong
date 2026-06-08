package e2e

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/xnslong/rc_xnslong/internal/config"
)

const defaultMQURL = "amqp://notify:notify@localhost:5672/"

// Suite holds the shared E2E test infrastructure.
type Suite struct {
	ServerURL   string
	MockVendors map[string]*MockVendor // vendorID → MockVendor

	notifCmd    *exec.Cmd
	mockVendors []*MockVendor
	cleanups    []func()
}

// SetupSuite initializes connections, starts MockVendors, and launches
// notification-server as a subprocess using the default testdata configuration.
func SetupSuite() (*Suite, error) {
	_, filename, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(filename), "testdata", "common")
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")

	loader, err := config.NewLoader(testdataDir)
	if err != nil {
		return nil, fmt.Errorf("create config loader: %w", err)
	}
	if err := loader.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return SetupSuiteWithConfig(testdataDir, projectRoot, loader.GetAllVendorIDs())
}

// SetupSuiteWithConfig creates a Suite with the given config directory, project root,
// and list of vendor IDs to start MockVendors for.
func SetupSuiteWithConfig(configDir, projectRoot string, vendorIDs []string) (*Suite, error) {
	s := &Suite{}

	mqURL := envOrDefault("E2E_MQ_URL", defaultMQURL)

	if err := s.initNotificationServer(configDir, projectRoot, mqURL, vendorIDs); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Suite) initNotificationServer(configDir, projectRoot, mqURL string, vendorIDs []string) error {
	loader, err := config.NewLoader(configDir)
	if err != nil {
		return fmt.Errorf("create config loader: %w", err)
	}
	if err := loader.Load(context.Background()); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := s.startMockVendors(loader, vendorIDs); err != nil {
		return err
	}
	return s.startServerProcess(projectRoot, configDir, mqURL)
}

func (s *Suite) startMockVendors(loader *config.Loader, vendorIDs []string) error {
	s.MockVendors = make(map[string]*MockVendor)
	for _, vendorID := range vendorIDs {
		vendorCfg, err := loader.GetVendorConfig(vendorID)
		if err != nil {
			return fmt.Errorf("vendor config not found: %w", err)
		}

		u, err := url.Parse(vendorCfg.BaseURL)
		if err != nil {
			return fmt.Errorf("parse vendor base URL %q: %w", vendorCfg.BaseURL, err)
		}

		mv := NewMockVendor()
		if err := mv.Start(":" + u.Port()); err != nil {
			return fmt.Errorf("start mock vendor %s: %w", vendorID, err)
		}
		s.MockVendors[vendorID] = mv
		s.mockVendors = append(s.mockVendors, mv)
	}
	return nil
}

func (s *Suite) startServerProcess(projectRoot, configDir, mqURL string) error {
	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		return fmt.Errorf("build notification-server: %w", err)
	}

	s.ServerURL = "http://localhost:8080"
	s.notifCmd = exec.Command(binaryPath,
		"--config-dir="+configDir,
		"--http-addr=:8080",
	)
	s.notifCmd.Env = append(os.Environ(),
		"MQ_URL="+mqURL,
	)
	s.notifCmd.Dir = projectRoot
	s.notifCmd.Stdout = os.Stdout
	s.notifCmd.Stderr = os.Stderr

	if err := s.notifCmd.Start(); err != nil {
		return fmt.Errorf("start notification-server: %w", err)
	}

	if err := waitForHealth(s.ServerURL+"/healthz", 10*time.Second); err != nil {
		s.stopNotificationServer()
		return fmt.Errorf("notification-server health check: %w", err)
	}
	return nil
}

// TearDownSuite cleans up all resources: stops notification-server,
// stops MockVendors, closes connections.
func (s *Suite) TearDownSuite() {
	s.stopNotificationServer()

	for _, mv := range s.mockVendors {
		mv.Close()
	}

	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
}

// StopServer sends SIGTERM to the notification-server subprocess and waits
// for it to exit (up to the default 5s timeout).
func (s *Suite) StopServer() {
	s.stopNotificationServer()
}

func (s *Suite) stopNotificationServer() {
	if s.notifCmd == nil || s.notifCmd.Process == nil {
		return
	}

	log.Printf("sending SIGTERM to notification-server (pid %d)", s.notifCmd.Process.Pid)
	s.notifCmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		done <- s.notifCmd.Wait()
	}()

	select {
	case <-done:
		log.Printf("notification-server stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Printf("notification-server did not stop in time, killing")
		s.notifCmd.Process.Kill()
		<-done
	}
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

// eventSchemaFile is the YAML representation of an event schema file.
type eventSchemaFile struct {
	EventType   string         `yaml:"event_type"`
	Description string         `yaml:"description"`
	Version     int            `yaml:"version"`
	Schema      map[string]any `yaml:"schema"`
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
