package port

import "errors"

// ErrNotConfigured is returned when a requested configuration is not found.
var ErrNotConfigured = errors.New("config not configured")

// AuthConfig defines how to authenticate API requests to a vendor.
type AuthConfig struct {
	Type   string
	Config map[string]any
}

// VendorConfig holds vendor-level settings.
// Judgment is the vendor-wide default; per-event-type override via DeliverySpec.
type VendorConfig struct {
	VendorID string
	BaseURL  string
	Auth     *AuthConfig
	Retry    RetryPolicy
	Judgment ResponseJudgment
}

// RequestConfig defines the API call parameters for a specific delivery contract.
type RequestConfig struct {
	Method  string
	Path    string
	Headers map[string]string
}

// RetryPolicy defines the exponential backoff + jitter strategy.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelayMs int
	MaxDelayMs  int
	Multiplier  float64
	Jitter      float64
}

// RoutingRule maps an event type to a target vendor.
type RoutingRule struct {
	EventType string
	VendorID  string
}

// BodyConfig defines how to build the request body.
type BodyConfig struct {
	Type         string
	Template     map[string]any
	PluginName   string
	PluginConfig any
}

// MappingConfig defines how to transform a notification payload into an HTTP
// request for a specific (vendor, event_type) channel.
type MappingConfig struct {
	EventType string
	Request   RequestConfig
	Body      BodyConfig
}

// ResponseRule defines how to judge a single response outcome.
type ResponseRule struct {
	// JudgeType is one of: "http_status", "body_field", "body_match", "field_absent"
	JudgeType string

	// Field is the JSON field path to check (used by body_field / field_absent).
	Field string

	// Expected is the expected value (used by body_field / body_match).
	Expected any

	// ExpectedStatus is the expected HTTP status code (used by http_status).
	ExpectedStatus int
}

// ResponseJudgment defines vendor-specific success/retry/dead-letter rules.
// At least one SuccessRule must match for delivery to be SUCCEEDED.
// If no SuccessRule matches but a RetryableRule matches, delivery is retried.
// If neither matches, delivery goes to DEAD_LETTER.
type ResponseJudgment struct {
	SuccessRules   []ResponseRule
	RetryableRules []ResponseRule
}

// DeliverySpec describes the complete specification for delivering one
// notification to one vendor for a given event type.
// Judgment is optional; nil means fall back to VendorConfig.Judgment.
// Retry is optional; nil means fall back to VendorConfig.Retry.
type DeliverySpec struct {
	Mapping  MappingConfig
	Judgment *ResponseJudgment // nil → fallback to VendorConfig.Judgment
	Retry    *RetryPolicy      // nil → fallback to VendorConfig.Retry
	// Sign  *SignConfig       // future: request signing
}

// ConfigProvider is the central configuration accessor.
type ConfigProvider interface {
	GetVendorConfig(vendorID string) (*VendorConfig, error)
	GetDeliverySpec(vendorID, eventType string) (*DeliverySpec, error)
	GetRoutingRules(eventType string) ([]RoutingRule, error)
	GetEventSchema(eventType string) (map[string]any, error)
}
