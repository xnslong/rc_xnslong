package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"
)

const (
	triggerExchange  = "notification.trigger"
	triggerQueue     = "notification.trigger.q"
	deliveryExchange = "notification.delivery"
	deliveryQueue    = "notification.delivery.q"
	dlxExchange      = "notification.dlx"
	retryQueue       = "notification.retry.q"
)

type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func NewClient(url string) (*Client, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}

	if err := ch.ExchangeDeclare(triggerExchange, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare trigger exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(triggerQueue, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare trigger queue: %w", err)
	}
	if err := ch.QueueBind(triggerQueue, "#", triggerExchange, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("bind trigger queue: %w", err)
	}

	// Declare delivery exchange
	if err := ch.ExchangeDeclare(deliveryExchange, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare delivery exchange: %w", err)
	}

	// Declare delivery queue with DLX → dlxExchange
	deliveryArgs := amqp.Table{
		"x-dead-letter-exchange": dlxExchange,
	}
	if _, err := ch.QueueDeclare(deliveryQueue, true, false, false, false, deliveryArgs); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare delivery queue: %w", err)
	}
	if err := ch.QueueBind(deliveryQueue, "#", deliveryExchange, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("bind delivery queue: %w", err)
	}

	// Declare DLX exchange
	if err := ch.ExchangeDeclare(dlxExchange, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare dlx exchange: %w", err)
	}

	// Declare retry queue with DLX → deliveryExchange, per-message TTL via expiration
	retryArgs := amqp.Table{
		"x-dead-letter-exchange": deliveryExchange,
	}
	if _, err := ch.QueueDeclare(retryQueue, true, false, false, false, retryArgs); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("declare retry queue: %w", err)
	}
	if err := ch.QueueBind(retryQueue, "#", dlxExchange, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("bind retry queue: %w", err)
	}

	return &Client{conn: conn, ch: ch}, nil
}

func (c *Client) Close() {
	c.ch.Close()
	c.conn.Close()
}

func (c *Client) PublishTrigger(ctx context.Context, notificationID string) error {
	msg := amqp.Publishing{
		ContentType: "text/plain",
		Body:        []byte(notificationID),
	}
	if err := c.ch.PublishWithContext(ctx, triggerExchange, "", false, false, msg); err != nil {
		return fmt.Errorf("publish trigger: %w", err)
	}
	log.Info().Str("id", notificationID).Msg("trigger published to MQ")
	return nil
}

func (c *Client) PublishDelivery(ctx context.Context, vendorID, deliveryTaskID string) error {
	body, err := json.Marshal(map[string]string{
		"delivery_task_id": deliveryTaskID,
		"vendor_id":        vendorID,
	})
	if err != nil {
		return fmt.Errorf("marshal delivery message: %w", err)
	}

	msg := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}

	if err := c.ch.PublishWithContext(ctx, deliveryExchange, "", false, false, msg); err != nil {
		return fmt.Errorf("publish delivery: %w", err)
	}

	log.Info().
		Str("delivery_task_id", deliveryTaskID).
		Str("vendor_id", vendorID).
		Msg("delivery published to MQ")

	return nil
}

func (c *Client) PublishDelayed(ctx context.Context, vendorID, deliveryTaskID string, delayMs int) error {
	body, err := json.Marshal(map[string]string{
		"delivery_task_id": deliveryTaskID,
		"vendor_id":        vendorID,
	})
	if err != nil {
		return fmt.Errorf("marshal delayed message: %w", err)
	}

	msg := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Headers: amqp.Table{
			"x-original-routing-key": "delivery",
		},
		Expiration: strconv.Itoa(delayMs),
		Body:       body,
	}

	if err := c.ch.PublishWithContext(ctx, dlxExchange, "", false, false, msg); err != nil {
		return fmt.Errorf("publish delayed: %w", err)
	}

	log.Info().
		Str("delivery_task_id", deliveryTaskID).
		Str("vendor_id", vendorID).
		Int("delay_ms", delayMs).
		Msg("delayed delivery published to MQ")

	return nil
}

// Consume starts consuming messages from the specified queue.
// It wraps amqp.Channel.Consume for the given queue name.
func (c *Client) Consume(queue, consumer string, autoAck bool) (<-chan amqp.Delivery, error) {
	msgs, err := c.ch.Consume(queue, consumer, autoAck, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume %s: %w", queue, err)
	}
	return msgs, nil
}
