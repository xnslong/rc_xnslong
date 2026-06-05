package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// Status constants for delivery task life cycle.
const (
	StatusDelivering = "DELIVERING"
	StatusSucceeded  = "SUCCEEDED"
	StatusDeadLetter = "DEAD_LETTER"
)

// RequestBuilder builds HTTP requests for vendor API calls.
type RequestBuilder interface {
	BuildRequest(vendorCfg *port.VendorConfig, mappingCfg *port.MappingConfig, payload map[string]any) (*http.Request, error)
}

// WorkerDeps holds the dependencies for a WorkerPool.
type WorkerDeps struct {
	DB             port.DBClient
	MQ             port.MQClient
	Config         port.ConfigProvider
	Engine         RequestBuilder
	HTTPClient     *http.Client
	DeliveryEvents <-chan amqp.Delivery
}

// WorkerPool manages a pool of workers that consume delivery messages from MQ,
// call vendor APIs, and handle retries (MQ DLX+TTL) and dead letters.
//
// MVP scope: shared pool for all vendors, no rate limiting or circuit breaker.
type WorkerPool struct {
	concurrency int
	deps        WorkerDeps

	cancel    context.CancelFunc
	closeCh   chan struct{} // signals workers to stop without cancelling ctx
	wg        sync.WaitGroup
	processWg sync.WaitGroup
}

// NewWorkerPool creates a new worker pool with the given concurrency and dependencies.
func NewWorkerPool(concurrency int, deps WorkerDeps) *WorkerPool {
	return &WorkerPool{
		concurrency: concurrency,
		deps:        deps,
		closeCh:     make(chan struct{}),
	}
}

// Start launches the worker pool's consumption loop.
// Each worker independently consumes from the shared delivery queue.
func (p *WorkerPool) Start(ctx context.Context) error {
	if p.deps.DeliveryEvents == nil {
		return fmt.Errorf("DeliveryEvents channel is nil")
	}

	ctx, p.cancel = context.WithCancel(ctx)

	for i := 0; i < p.concurrency; i++ {
		p.wg.Add(1)
		go func(workerID int) {
			defer p.wg.Done()
			log.Printf("worker %d started", workerID)

			for {
				select {
				case <-ctx.Done():
					log.Printf("worker %d stopped (ctx cancelled)", workerID)
					return
				case <-p.closeCh:
					log.Printf("worker %d stopped (close signal)", workerID)
					return
				case msg, ok := <-p.deps.DeliveryEvents:
					if !ok {
						log.Printf("worker %d: delivery events channel closed", workerID)
						return
					}

					var body struct {
						DeliveryTaskID string `json:"delivery_task_id"`
						VendorID       string `json:"vendor_id"`
					}
					if err := json.Unmarshal(msg.Body, &body); err != nil {
						log.Printf("failed to parse delivery message: %v", err)
						msg.Nack(false, false)
						continue
					}

					if err := p.ProcessMessage(ctx, body.DeliveryTaskID); err != nil {
						log.Printf("delivery task %s failed: %v", body.DeliveryTaskID, err)
						msg.Nack(false, false)
					} else {
						msg.Ack(false)
					}
				}
			}
		}(i)
	}

	return nil
}

// Stop gracefully shuts down the worker pool.
// It signals workers to stop consuming, then waits for in-flight deliveries
// to complete before returning. The worker context is NOT cancelled so that
// in-flight deliveries can still access the DB. If the timeout fires before
// workers finish, the context is cancelled as a fallback.
func (p *WorkerPool) Stop(ctx context.Context) error {
	close(p.closeCh) // signal workers to stop (context stays valid for DB ops)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		p.processWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Fallback: cancel context so workers stop even if channel blocks
		if p.cancel != nil {
			p.cancel()
		}
		// Wait for workers to finish after cancellation
		p.wg.Wait()
		p.processWg.Wait()
		return ctx.Err()
	}
}

// ProcessMessage processes a single delivery task end-to-end:
// fetches the task and payload, builds the vendor request via Engine,
// executes the HTTP call, and handles the outcome (success / retry / dead-letter).
//
// Returning nil signals the caller that the message should be acknowledged (ACK).
// Returning an error signals the caller that the message should be requeued (NACK).
func (p *WorkerPool) ProcessMessage(ctx context.Context, deliveryTaskID string) error {
	p.processWg.Add(1)
	defer p.processWg.Done()

	// 1. Fetch delivery task
	task, err := p.deps.DB.GetDeliveryTask(ctx, deliveryTaskID)
	if err != nil {
		return fmt.Errorf("get delivery task: %w", err)
	}

	// 2. Fetch notification payload
	payload, err := p.deps.DB.GetNotificationPayload(ctx, task.NotificationID)
	if err != nil {
		return fmt.Errorf("get notification payload: %w", err)
	}

	// 3. Get vendor configuration
	vendorCfg, err := p.deps.Config.GetVendorConfig(task.VendorID)
	if err != nil {
		log.Printf("vendor config unavailable for %s: %v", task.VendorID, err)
		return p.handleDeadLetter(ctx, task, err.Error())
	}

	// 4. Get delivery spec (mapping config)
	spec, err := p.deps.Config.GetDeliverySpec(task.VendorID, task.EventType)
	if err != nil {
		log.Printf("delivery spec unavailable for %s/%s: %v", task.VendorID, task.EventType, err)
		return p.handleDeadLetter(ctx, task, err.Error())
	}

	// Retry policy: contract override takes precedence, otherwise vendor default
	retryPolicy := vendorCfg.Retry
	if spec.Retry != nil {
		retryPolicy = *spec.Retry
	}

	// 5. Build HTTP request via Engine
	req, err := p.deps.Engine.BuildRequest(vendorCfg, &spec.Mapping, payload)
	if err != nil {
		log.Printf("delivery task %s failed to build request: %v", task.ID, err)
		return p.retryOrDeadLetter(ctx, task, retryPolicy, err.Error())
	}

	// 6. Execute HTTP request
	resp, err := p.deps.HTTPClient.Do(req)
	if err != nil {
		return p.handleHTTPError(ctx, task, retryPolicy, err.Error())
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 7. Judge response outcome
	// For the skeleton, use basic HTTP status code judgment.
	// Full ResponseJudgment rule matching will be implemented later.
	if isHTTPSuccess(resp) {
		return p.handleDeliverySuccess(ctx, task)
	}

	if isHTTPRetryable(resp) {
		return p.retryOrDeadLetter(ctx, task, retryPolicy, resp.Status)
	}

	return p.handleDeadLetter(ctx, task, resp.Status)
}

func isHTTPSuccess(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func isHTTPRetryable(resp *http.Response) bool {
	return resp.StatusCode >= 500 || resp.StatusCode == 429
}

func (p *WorkerPool) handleDeliverySuccess(ctx context.Context, task *model.DeliveryTask) error {
	err := p.deps.DB.UpdateDeliveryTaskStatus(ctx, task.ID, StatusSucceeded)
	if err != nil {
		log.Printf("delivery task %s succeeded but DB status update failed: %v", task.ID, err)
	}
	// Check if the notification is fully resolved
	p.resolveNotificationStatus(ctx, task.NotificationID)
	return nil // ACK even if DB update fails — vendor already received the request
}

func (p *WorkerPool) handleHTTPError(ctx context.Context, task *model.DeliveryTask, retryPolicy port.RetryPolicy, errMsg string) error {
	return p.retryOrDeadLetter(ctx, task, retryPolicy, errMsg)
}

func (p *WorkerPool) retryOrDeadLetter(ctx context.Context, task *model.DeliveryTask, retryPolicy port.RetryPolicy, errMsg string) error {
	nextRetry := task.RetryCount + 1

	if nextRetry >= retryPolicy.MaxAttempts {
		return p.handleDeadLetter(ctx, task, errMsg)
	}

	now := time.Now()
	delay := calculateBackoff(nextRetry, retryPolicy)
	nextRetryAt := now.Add(delay)

	err := p.deps.DB.UpdateDeliveryTaskRetry(ctx, task.ID, nextRetry, nextRetryAt, errMsg)
	if err != nil {
		return fmt.Errorf("update delivery task retry: %w", err)
	}

	err = p.deps.MQ.PublishDelayed(ctx, task.VendorID, task.ID, int(delay.Milliseconds()))
	if err != nil {
		return fmt.Errorf("publish delayed: %w", err)
	}

	return nil
}

func (p *WorkerPool) handleDeadLetter(ctx context.Context, task *model.DeliveryTask, errMsg string) error {
	err := p.deps.DB.UpdateDeliveryTaskStatus(ctx, task.ID, StatusDeadLetter)
	if err != nil {
		return fmt.Errorf("update delivery task status to dead_letter: %w", err)
	}

	err = p.deps.DB.InsertDeadLetter(ctx, task, errMsg)
	if err != nil {
		return fmt.Errorf("insert dead letter: %w", err)
	}

	// Check if the notification is fully resolved
	p.resolveNotificationStatus(ctx, task.NotificationID)

	return nil
}

// resolveNotificationStatus checks if all delivery tasks for a notification have
// reached a terminal state and updates the notification status accordingly.
func (p *WorkerPool) resolveNotificationStatus(ctx context.Context, notificationID string) {
	total, succeeded, deadLetter, err := p.deps.DB.GetDeliveryTaskCounts(ctx, notificationID)
	if err != nil {
		log.Printf("failed to get delivery task counts for notification %s: %v", notificationID, err)
		return
	}

	terminalCount := succeeded + deadLetter
	if terminalCount < total {
		return // still waiting for other tasks
	}

	// All tasks have reached a terminal state — determine notification status
	if deadLetter == 0 {
		err = p.deps.DB.UpdateNotificationStatus(ctx, notificationID, "SUCCEEDED")
	} else if succeeded == 0 {
		err = p.deps.DB.UpdateNotificationStatus(ctx, notificationID, "FAILED")
	} else {
		err = p.deps.DB.UpdateNotificationStatus(ctx, notificationID, "PARTIALLY_FAILED")
	}
	if err != nil {
		log.Printf("failed to update notification %s status: %v", notificationID, err)
	}
}
