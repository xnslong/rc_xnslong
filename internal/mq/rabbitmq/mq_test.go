//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_PublishTrigger(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("publish trigger message is received by trigger queue consumer", func(t *testing.T) {
		msgs := consumeOnTempConn(t, TriggerQueue, "", true)

		err := rmqClient.PublishTrigger(ctx, "notif-123")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, "notif-123", string(msg.Body))
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for trigger message")
		}
	})

	t.Run("publish trigger sets correct body", func(t *testing.T) {
		msgs := consumeOnTempConn(t, TriggerQueue, "", true)

		err := rmqClient.PublishTrigger(ctx, "notif-456")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, "text/plain", msg.ContentType)
			assert.Equal(t, "notif-456", string(msg.Body))
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for trigger message")
		}
	})
}

func TestClient_PublishDelivery(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("publish delivery message is received by delivery queue consumer", func(t *testing.T) {
		msgs := consumeOnTempConn(t, DeliveryQueue, "", true)

		err := rmqClient.PublishDelivery(ctx, "vendor-1", "task-001")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-001", body["delivery_task_id"])
			assert.Equal(t, "vendor-1", body["vendor_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for delivery message")
		}
	})

	t.Run("publish delivery message has correct JSON body with delivery_task_id and vendor_id", func(t *testing.T) {
		msgs := consumeOnTempConn(t, DeliveryQueue, "", true)

		err := rmqClient.PublishDelivery(ctx, "vendor-2", "task-002")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, "application/json", msg.ContentType)

			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-002", body["delivery_task_id"])
			assert.Equal(t, "vendor-2", body["vendor_id"])
			assert.Len(t, body, 2)
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for delivery message")
		}
	})

	t.Run("publish delivery message is persistent", func(t *testing.T) {
		msgs := consumeOnTempConn(t, DeliveryQueue, "", true)

		err := rmqClient.PublishDelivery(ctx, "vendor-3", "task-003")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, amqp.Persistent, msg.DeliveryMode)
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for delivery message")
		}
	})
}

func TestClient_PublishDelayed(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("publish delayed message sets expiration header", func(t *testing.T) {
		msgs := consumeOnTempConn(t, RetryQueue, "", true)

		err := rmqClient.PublishDelayed(ctx, "vendor-4", "task-004", 5000)
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, "5000", msg.Expiration)
			assert.Equal(t, "application/json", msg.ContentType)

			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-004", body["delivery_task_id"])
			assert.Equal(t, "vendor-4", body["vendor_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for delayed message")
		}
	})
}

func TestClient_Consume(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("consume returns delivery channel for given queue", func(t *testing.T) {
		conn, err := amqp.Dial(rmqURL)
		require.NoError(t, err)
		defer conn.Close()
		ch, err := conn.Channel()
		require.NoError(t, err)
		defer ch.Close()

		msgs, err := ch.Consume(DeliveryQueue, "", true, false, false, false, nil)
		require.NoError(t, err)
		require.NotNil(t, msgs)

		err = rmqClient.PublishDelivery(ctx, "vendor-5", "task-005")
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-005", body["delivery_task_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for consumed message")
		}
	})
}

// consumeOnTempConn creates a temporary connection+channel, starts consuming
// on the given queue with autoAck, and cleans up via t.Cleanup.
func consumeOnTempConn(t *testing.T, queue, consumer string, autoAck bool) <-chan amqp.Delivery {
	conn, err := amqp.Dial(rmqURL)
	require.NoError(t, err)
	ch, err := conn.Channel()
	require.NoError(t, err)

	msgs, err := ch.Consume(queue, consumer, autoAck, false, false, false, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		ch.Close()
		conn.Close()
	})

	return msgs
}
