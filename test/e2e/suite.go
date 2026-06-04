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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
	"gopkg.in/yaml.v3"

	"github.com/xnslong/rc_xnslong/internal/config"
)

const (
	TriggerExchange  = "notification.trigger"
	TriggerQueue     = "notification.trigger.q"
	DeliveryExchange = "notification.delivery"
	DeliveryQueue    = "notification.delivery.q"
	DlxExchange      = "notification.dlx"
	RetryQueue       = "notification.retry.q"

	defaultPGURL = "postgres://notify:notify@localhost:5432/notification?sslmode=disable"
	defaultMQURL = "amqp://notify:notify@localhost:5672/"
)

// Suite holds the shared E2E test infrastructure.
type Suite struct {
	DBPool     *pgxpool.Pool
	MQConn     *amqp.Connection
	MQChan     *amqp.Channel
	ServerURL  string
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

	// Load config to discover all vendor IDs
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

	pgURL := envOrDefault("E2E_PG_URL", defaultPGURL)
	mqURL := envOrDefault("E2E_MQ_URL", defaultMQURL)

	// Connect to PostgreSQL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		return nil, fmt.Errorf("connect pg: %w", err)
	}
	s.DBPool = pool
	s.cleanups = append(s.cleanups, pool.Close)

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping pg: %w", err)
	}

	// Clean DB state from previous test runs
	// Note: event_schemas table is no longer used by the server — schemas
	// are loaded from local config files via ConfigLoader.
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE delivery_tasks, notifications CASCADE"); err != nil {
		return nil, fmt.Errorf("clean db: %w", err)
	}

	// Connect to RabbitMQ
	conn, err := amqp.Dial(mqURL)
	if err != nil {
		return nil, fmt.Errorf("connect mq: %w", err)
	}
	s.MQConn = conn
	s.cleanups = append(s.cleanups, func() { conn.Close() })

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open mq channel: %w", err)
	}
	s.MQChan = ch
	s.cleanups = append(s.cleanups, func() { ch.Close() })

	// Declare MQ topology (clean slate)
	if err := declareMQTopology(ch); err != nil {
		return nil, fmt.Errorf("declare mq topology: %w", err)
	}

	// Purge queues to remove stale messages
	if _, err := ch.QueuePurge(TriggerQueue, false); err != nil {
		return nil, fmt.Errorf("purge trigger queue: %w", err)
	}
	if _, err := ch.QueuePurge(DeliveryQueue, false); err != nil {
		return nil, fmt.Errorf("purge delivery queue: %w", err)
	}
	if _, err := ch.QueuePurge(RetryQueue, false); err != nil {
		return nil, fmt.Errorf("purge retry queue: %w", err)
	}

	// Load vendor config to discover ports
	loader, err := config.NewLoader(configDir)
	if err != nil {
		return nil, fmt.Errorf("create config loader: %w", err)
	}
	if err := loader.Load(ctx); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Create one MockVendor per vendor, using ports from config
	s.MockVendors = make(map[string]*MockVendor)
	for _, vendorID := range vendorIDs {
		vendorCfg, ok := loader.GetVendorConfig(vendorID)
		if !ok {
			return nil, fmt.Errorf("vendor config not found: %s", vendorID)
		}

		u, err := url.Parse(vendorCfg.Request.URLTmpl)
		if err != nil {
			return nil, fmt.Errorf("parse vendor URL %q: %w", vendorCfg.Request.URLTmpl, err)
		}
		vendorAddr := ":" + u.Port()

		mv := NewMockVendor()
		if err := mv.Start(vendorAddr); err != nil {
			return nil, fmt.Errorf("start mock vendor %s: %w", vendorID, err)
		}
		s.MockVendors[vendorID] = mv
		s.mockVendors = append(s.mockVendors, mv)
	}

	// Build notification-server binary if not already built
	binaryPath, err := BuildBinary(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build notification-server: %w", err)
	}

	// Start notification-server as subprocess
	s.ServerURL = "http://localhost:8080"
	s.notifCmd = exec.Command(binaryPath,
		"--config-dir="+configDir,
		"--http-addr=:8080",
	)
	s.notifCmd.Env = append(os.Environ(),
		"PG_URL="+pgURL,
		"MQ_URL="+mqURL,
	)
	s.notifCmd.Dir = projectRoot
	s.notifCmd.Stdout = os.Stdout
	s.notifCmd.Stderr = os.Stderr

	if err := s.notifCmd.Start(); err != nil {
		return nil, fmt.Errorf("start notification-server: %w", err)
	}

	// Wait for health endpoint
	if err := waitForHealth(s.ServerURL+"/healthz", 10*time.Second); err != nil {
		s.stopNotificationServer()
		return nil, fmt.Errorf("notification-server health check: %w", err)
	}

	return s, nil
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
// for it to exit (up to the default 5s timeout). This is a controlled shutdown
// without cleaning up MockVendors or connections, useful for shutdown tests.
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

// AwaitMQConsume consumes a single message from a queue with timeout.
func (s *Suite) AwaitMQConsume(queue string, timeout time.Duration) (*amqp.Delivery, error) {
	msgs, err := s.MQChan.Consume(queue, "e2e-"+queue, true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume %s: %w", queue, err)
	}

	select {
	case d := <-msgs:
		return &d, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for message on %s", queue)
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

// declareMQTopology sets up the MQ exchanges, queues, and bindings.
func declareMQTopology(ch *amqp.Channel) error {
	// Delete existing queues to avoid PRECONDITION_FAILED
	ch.QueueDelete(TriggerQueue, false, false, false)
	ch.QueueDelete(DeliveryQueue, false, false, false)
	ch.QueueDelete(RetryQueue, false, false, false)

	// Trigger exchange + queue
	if err := ch.ExchangeDeclare(TriggerExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare trigger exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(TriggerQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare trigger queue: %w", err)
	}
	if err := ch.QueueBind(TriggerQueue, "#", TriggerExchange, false, nil); err != nil {
		return fmt.Errorf("bind trigger queue: %w", err)
	}

	// Delivery exchange + queue (DLX → notification.dlx)
	if err := ch.ExchangeDeclare(DeliveryExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare delivery exchange: %w", err)
	}
	deliveryArgs := amqp.Table{
		"x-dead-letter-exchange": DlxExchange,
	}
	if _, err := ch.QueueDeclare(DeliveryQueue, true, false, false, false, deliveryArgs); err != nil {
		return fmt.Errorf("declare delivery queue: %w", err)
	}
	if err := ch.QueueBind(DeliveryQueue, "#", DeliveryExchange, false, nil); err != nil {
		return fmt.Errorf("bind delivery queue: %w", err)
	}

	// DLX exchange + retry queue
	if err := ch.ExchangeDeclare(DlxExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dlx exchange: %w", err)
	}
	retryArgs := amqp.Table{
		"x-dead-letter-exchange": DeliveryExchange,
	}
	if _, err := ch.QueueDeclare(RetryQueue, true, false, false, false, retryArgs); err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	}
	if err := ch.QueueBind(RetryQueue, "#", DlxExchange, false, nil); err != nil {
		return fmt.Errorf("bind retry queue: %w", err)
	}

	return nil
}

// eventSchemaFile is the YAML representation of an event schema file.
type eventSchemaFile struct {
	EventType   string         `yaml:"event_type"`
	Description string         `yaml:"description"`
	Version     int            `yaml:"version"`
	Schema      map[string]any `yaml:"schema"`
}

// seedEventSchemas reads schema YAML files from configDir/event_schemas/ and inserts
// them into the event_schemas table.
func seedEventSchemas(ctx context.Context, pool *pgxpool.Pool, configDir string) error {
	schemasDir := filepath.Join(configDir, "event_schemas")
	entries, err := os.ReadDir(schemasDir)
	if err != nil {
		return fmt.Errorf("read event_schemas dir %s: %w", schemasDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(schemasDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read schema file %s: %w", entry.Name(), err)
		}

		var sf eventSchemaFile
		if err := yaml.Unmarshal(data, &sf); err != nil {
			return fmt.Errorf("parse schema file %s: %w", entry.Name(), err)
		}

		schemaJSON, err := json.Marshal(sf.Schema)
		if err != nil {
			return fmt.Errorf("marshal schema %s to json: %w", entry.Name(), err)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO event_schemas (event_type, schema_def, description) VALUES ($1, $2::jsonb, $3) ON CONFLICT DO NOTHING`,
			sf.EventType, string(schemaJSON), sf.Description,
		); err != nil {
			return fmt.Errorf("seed schema %s: %w", sf.EventType, err)
		}
	}
	return nil
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

	// Build only if binary doesn't exist yet (cached across tests within a single run).
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
