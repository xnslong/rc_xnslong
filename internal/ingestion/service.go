package ingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/xnslong/rc_xnslong/internal/model"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// Service handles notification ingestion with schema validation.
type Service struct {
	db        port.DBClient
	mq        port.MQClient
	validator *schemaValidator
}

// NewService creates a new ingestion service.
func NewService(db port.DBClient, mq port.MQClient) *Service {
	return &Service{
		db:        db,
		mq:        mq,
		validator: newSchemaValidator(),
	}
}

// Submit accepts a notification request, validates the payload against the event
// type's schema, idempotently writes it to DB, and publishes a trigger message
// to MQ for new notifications.
func (s *Service) Submit(ctx context.Context, params model.UpsertParams) (*model.Notification, error) {
	// 1. Validate payload against event type schema
	if err := s.validateSchema(ctx, params.EventType, params.Payload); err != nil {
		return nil, err
	}

	// 2. Idempotent upsert
	id, isNew, err := s.db.UpsertNotification(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("upsert notification: %w", err)
	}

	if isNew {
		// 3. Publish trigger message for new notifications
		if err := s.mq.PublishTrigger(ctx, id); err != nil {
			log.Error().Err(err).Str("notification_id", id).Msg("failed to publish trigger message")
		}
	}

	return s.GetByID(ctx, id)
}

// GetByID retrieves a notification by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*model.Notification, error) {
	n, err := s.db.GetNotification(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get notification: %w", err)
	}
	return n, nil
}

// GetDeliveryTasks retrieves all delivery tasks for a notification.
func (s *Service) GetDeliveryTasks(ctx context.Context, notificationID string) ([]*model.DeliveryTask, error) {
	return s.db.GetDeliveryTasksByNotificationID(ctx, notificationID)
}

// validateSchema retrieves the schema for the event type and validates the payload.
// Returns ErrEventNotFound if the event type is not registered.
// Returns *SchemaValidationError if the payload does not match the schema.
func (s *Service) validateSchema(ctx context.Context, eventType string, payload map[string]any) error {
	schemaDef, err := s.db.GetEventSchema(ctx, eventType)
	if err != nil {
		return &ErrEventNotFound{EventType: eventType}
	}

	valErrs := s.validator.validate(schemaDef, payload)
	if len(valErrs) > 0 {
		details := make([]ValidationDetail, len(valErrs))
		for i, ve := range valErrs {
			details[i] = ValidationDetail{Field: ve.Field, Error: ve.Message}
		}
		return &SchemaValidationError{Details: details}
	}

	return nil
}

// IsErrEventNotFound checks if an error is ErrEventNotFound.
func IsErrEventNotFound(err error) bool {
	var e *ErrEventNotFound
	return errors.As(err, &e)
}

// IsSchemaValidationError checks if an error is SchemaValidationError.
func IsSchemaValidationError(err error) bool {
	var e *SchemaValidationError
	return errors.As(err, &e)
}

// GetSchemaValidationDetails extracts validation details from the error.
func GetSchemaValidationDetails(err error) []ValidationDetail {
	var e *SchemaValidationError
	if errors.As(err, &e) {
		return e.Details
	}
	return nil
}
