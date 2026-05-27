//go:build integration

package rabbitmq

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	rmqClient      *Client
	rmqAdminCh     *amqp.Channel
	rmqURL         string
	rmqContainerID string
)

func TestMain(m *testing.M) {
	containerID, url, err := startRabbitMQContainer()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to start rabbitmq: %v\n", err)
		os.Exit(1)
	}
	defer stopContainer(containerID)
	rmqContainerID = containerID

	client, err := NewClient(url)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create client: %v\n", err)
		os.Exit(1)
	}
	rmqClient = client
	rmqURL = url

	conn, err := amqp.Dial(url)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to dial admin: %v\n", err)
		os.Exit(1)
	}
	rmqAdminCh, err = conn.Channel()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create admin channel: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	rmqAdminCh.Close()
	conn.Close()
	rmqClient.Close()
	os.Exit(code)
}

// --- docker helpers ---

func startRabbitMQContainer() (containerID, url string, err error) {
	cmd := exec.Command("docker", "run", "-d", "-P",
		"-e", "RABBITMQ_DEFAULT_USER=notify",
		"-e", "RABBITMQ_DEFAULT_PASS=notify",
		"rabbitmq:4-management",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("docker run: %w", err)
	}
	containerID = strings.TrimSpace(string(out))

	hostPort, err := getHostPort(containerID, "5672/tcp")
	if err != nil {
		stopContainer(containerID)
		return "", "", fmt.Errorf("get host port: %w", err)
	}

	url = fmt.Sprintf("amqp://notify:notify@localhost:%s/", hostPort)

	if err := waitForRabbitMQ(url, 30*time.Second); err != nil {
		stopContainer(containerID)
		return "", "", err
	}

	return containerID, url, nil
}

func getHostPort(containerID, containerPort string) (string, error) {
	out, err := exec.Command("docker", "port", containerID, containerPort).Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	idx := strings.LastIndex(lines[0], ":")
	if idx < 0 {
		return "", fmt.Errorf("unexpected docker port output: %s", lines[0])
	}
	return lines[0][idx+1:], nil
}

func stopContainer(id string) {
	_ = exec.Command("docker", "rm", "-f", id).Run()
}

func waitForRabbitMQ(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := amqp.Dial(url)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		conn.Close()
		return nil
	}
	return fmt.Errorf("rabbitmq not ready within %v", timeout)
}

func purgeRMQQueues(t *testing.T) {
	for _, q := range []string{DeliveryQueue, TriggerQueue, RetryQueue} {
		_, err := rmqAdminCh.QueuePurge(q, false)
		if err != nil {
			t.Logf("purge queue %s: %v", q, err)
		}
	}
}
