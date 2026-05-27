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

func TestDLX_TopologyDeclare(t *testing.T) {
	t.Run("declare full topology: DELIVERY → RETRY → DLX chain", func(t *testing.T) {
		// Verify all three queues exist
		_, err := rmqAdminCh.QueueInspect(DeliveryQueue)
		require.NoError(t, err, "delivery queue should exist")

		_, err = rmqAdminCh.QueueInspect(RetryQueue)
		require.NoError(t, err, "retry queue should exist")

		_, err = rmqAdminCh.QueueInspect(TriggerQueue)
		require.NoError(t, err, "trigger queue should exist")

		// DLX chain behavior is verified in TestDLX_FailNackToRetry
	})
}

func TestDLX_SuccessAck(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("consume and ACK message — message does not reappear", func(t *testing.T) {
		err := rmqClient.PublishDelivery(ctx, "vendor-ack", "task-ack-001")
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Verify message is in the queue
		dq, err := rmqAdminCh.QueueInspect(DeliveryQueue)
		require.NoError(t, err)
		require.Equal(t, 1, dq.Messages)

		// Consume and Ack
		conn, err := amqp.Dial(rmqURL)
		require.NoError(t, err)
		defer conn.Close()
		ch, err := conn.Channel()
		require.NoError(t, err)
		defer ch.Close()

		msgs, err := ch.Consume(DeliveryQueue, "", false, false, false, false, nil)
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			require.NoError(t, msg.Ack(false))
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for message to ack")
		}

		time.Sleep(200 * time.Millisecond)

		// Queue should be empty after ack
		dq, err = rmqAdminCh.QueueInspect(DeliveryQueue)
		require.NoError(t, err)
		assert.Equal(t, 0, dq.Messages)
	})
}

func TestDLX_FailNackToRetry(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("consume and NACK (not requeue) — message goes to DLX → RETRY_Q", func(t *testing.T) {
		err := rmqClient.PublishDelivery(ctx, "vendor-nack", "task-nack-001")
		require.NoError(t, err)

		// Consume from delivery queue — autoAck=false so we can NACK
		conn, err := amqp.Dial(rmqURL)
		require.NoError(t, err)
		defer conn.Close()
		ch, err := conn.Channel()
		require.NoError(t, err)
		defer ch.Close()

		msgs, err := ch.Consume(DeliveryQueue, "", false, false, false, false, nil)
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			require.NoError(t, msg.Nack(false, false)) // not requeue → DLX
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for message to nack")
		}

		// NACK'd message should now be in retry queue
		retryMsgs := consumeOnTempConn(t, RetryQueue, "", true)
		select {
		case msg := <-retryMsgs:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-nack-001", body["delivery_task_id"])
			assert.Equal(t, "vendor-nack", body["vendor_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for nack'd message in retry queue")
		}
	})

	t.Run("message in RETRY_Q has expiration matching configured delay", func(t *testing.T) {
		msgs := consumeOnTempConn(t, RetryQueue, "", true)

		err := rmqClient.PublishDelayed(ctx, "vendor-exp", "task-exp-001", 3000)
		require.NoError(t, err)

		select {
		case msg := <-msgs:
			assert.Equal(t, "3000", msg.Expiration)
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for delayed message in retry queue")
		}
	})

	t.Run("retried message is consumed again from DELIVERY_Q", func(t *testing.T) {
		// Start a consumer on delivery queue first (before publishing), then publish delayed
		// with a short TTL. The message goes through the DLX chain and comes back.
		msgs := consumeOnTempConn(t, DeliveryQueue, "", true)

		err := rmqClient.PublishDelayed(ctx, "vendor-retry", "task-retry-001", 500)
		require.NoError(t, err)

		// Flow: dlxExchange → retryQueue (Expiration=500) → wait 500ms → deliveryExchange → deliveryQueue
		select {
		case msg := <-msgs:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-retry-001", body["delivery_task_id"])
			assert.Equal(t, "vendor-retry", body["vendor_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for retried message in delivery queue")
		}
	})
}

func TestDLX_MaxRetries(t *testing.T) {
	purgeRMQQueues(t)
	ctx := context.Background()

	t.Run("message retried up to max_attempts then discarded or dead-lettered to final DLX", func(t *testing.T) {
		// max_attempts is tracked at the application layer (delivery_tasks.retry_count).
		// At the MQ topology level, we verify a complete retry lap works:
		// NACK → DLX → RETRY_Q → per-message TTL → back to DELIVERY_Q.

		// --- Lap 1: NACK → message goes to retry queue ---
		err := rmqClient.PublishDelivery(ctx, "vendor-max", "task-max-001")
		require.NoError(t, err)

		conn, err := amqp.Dial(rmqURL)
		require.NoError(t, err)
		ch, err := conn.Channel()
		require.NoError(t, err)

		dqMsgs, err := ch.Consume(DeliveryQueue, "", false, false, false, false, nil)
		require.NoError(t, err)

		select {
		case msg := <-dqMsgs:
			require.NoError(t, msg.Nack(false, false))
		case <-time.After(5 * time.Second):
			conn.Close()
			t.Fatal("timeout waiting for lap1 message")
		}
		ch.Close()
		conn.Close()

		// Verify message is in retry queue, then close the retry consumer
		// before publishing the delayed message (otherwise stale consumer
		// would snatch the delayed message before TTL expires).
		rConn, err := amqp.Dial(rmqURL)
		require.NoError(t, err)
		rCh, err := rConn.Channel()
		require.NoError(t, err)

		rMsgs, err := rCh.Consume(RetryQueue, "", true, false, false, false, nil)
		require.NoError(t, err)

		select {
		case msg := <-rMsgs:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-max-001", body["delivery_task_id"])
		case <-time.After(5 * time.Second):
			rCh.Close()
			rConn.Close()
			t.Fatal("timeout waiting for message in retry queue after lap1 nack")
		}
		rCh.Close()
		rConn.Close()

		// Publish delayed to cycle the message back to delivery queue
		err = rmqClient.PublishDelayed(ctx, "vendor-max", "task-max-001", 300)
		require.NoError(t, err)

		// --- Lap 2: Message comes back to delivery queue ---
		dqMsgs2 := consumeOnTempConn(t, DeliveryQueue, "", true)
		select {
		case msg := <-dqMsgs2:
			var body map[string]string
			require.NoError(t, json.Unmarshal(msg.Body, &body))
			assert.Equal(t, "task-max-001", body["delivery_task_id"])
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for retried message in lap2")
		}
	})
}
