package ingestion

import "fmt"

// SchemaValidationError contains details about payload schema validation failures.
type SchemaValidationError struct {
	Details []ValidationDetail
}

// ValidationDetail describes a single schema validation error.
type ValidationDetail struct {
	Field string `json:"field"`
	Error string `json:"error"`
}

func (e *SchemaValidationError) Error() string {
	return fmt.Sprintf("schema validation failed: %d error(s)", len(e.Details))
}

// ErrEventNotFound is returned when the event type is not registered.
type ErrEventNotFound struct {
	EventType string
}

func (e *ErrEventNotFound) Error() string {
	return fmt.Sprintf("event type %q not found", e.EventType)
}

// ValidationError describes a single validation failure from the validator.
// It mirrors a subset of what gojsonschema produces, in a library-agnostic way.
type ValidationError struct {
	Field   string
	Message string
}
