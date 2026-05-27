package e2e

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

// TestMQConnectivity verifies the MQ connection works end-to-end.
func TestMQConnectivity(t *testing.T) {
	conn, err := amqp.Dial("amqp://notify:notify@localhost:5672/")
	require.NoError(t, err)
	defer conn.Close()

	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	err = ch.ExchangeDeclare("notification.trigger", "topic", true, false, false, false, nil)
	require.NoError(t, err)

	_, err = ch.QueueDeclare("notification.trigger.q", true, false, false, false, nil)
	require.NoError(t, err)

	err = ch.QueueBind("notification.trigger.q", "#", "notification.trigger", false, nil)
	require.NoError(t, err)

	// Purge any stale messages
	_, err = ch.QueuePurge("notification.trigger.q", false)
	require.NoError(t, err)

	// Publish a test message
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ch.PublishWithContext(ctx, "notification.trigger", "", false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        []byte("test-message-123"),
	})
	require.NoError(t, err)

	// Consume it back
	msgs, err := ch.Consume("notification.trigger.q", "debug-consumer", true, false, false, false, nil)
	require.NoError(t, err)

	select {
	case d := <-msgs:
		require.Equal(t, "test-message-123", string(d.Body))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}
