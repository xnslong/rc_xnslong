package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"


	"github.com/rs/zerolog/log"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// Config type string constants used in recordError and loadDir.
const (
	configTypeVendor   = "vendor"
	configTypeContract = "contract"
	configTypeSchema   = "schema"
	configTypeRoute    = "route"
)

// Log attribute constants.
const (
	logModuleConfigLoader = "config.loader"
	logEventLoadError     = "load_config_error"
	logMsgConfigFailure   = "config load failure"
)

// Template reference prefixes.
const (
	refPrefixPayload = "@{payload:"
	refPrefixItem    = "@{item:"
	refItemBare      = "@{item}"
)

// Schema key constants.
const schemaKeyProperties = "properties"

// ---- YAML intermediate types ----

// routesFile is the YAML representation of events/{biz}/routes/{event}.yaml.
type routesFile struct {
	EventType string `yaml:"event_type"`
	Routes    []struct {
		VendorID string `yaml:"vendor_id"`
	} `yaml:"routes"`
}

// vendorAuthFile is the YAML type for the auth block in vendor.yaml.
type vendorAuthFile struct {
	Type   string         `yaml:"type"`
	Config map[string]any `yaml:"config"`
}

// vendorRetryFile is the YAML type for the retry_policy block.
type vendorRetryFile struct {
	MaxAttempts int     `yaml:"max_attempts"`
	BaseDelay   string  `yaml:"base_delay"`
	MaxDelay    string  `yaml:"max_delay"`
	Multiplier  float64 `yaml:"multiplier"`
	Jitter      float64 `yaml:"jitter"`
}

// vendorConfigFile is the YAML representation of a vendor config file
// located at vendors/{vendor}/vendor.yaml.
// This file only contains vendor-level settings that are independent
// of any specific event type.
type vendorConfigFile struct {
	VendorID         string                  `yaml:"vendor_id"`
	BaseURL          string                  `yaml:"base_url"`
	Auth             *vendorAuthFile         `yaml:"auth"`
	RetryPolicy      vendorRetryFile         `yaml:"retry_policy"`
	ResponseJudgment *vendorResponseJudgment `yaml:"response_judgment"`
}

// deliveryContractFile is the YAML representation of a delivery contract
// located at vendors/{vendor}/{biz}/{event}.yaml.
// Defines the complete API call parameters (method, path, headers, body)
// for a specific (vendor, event_type) combination.
type deliveryContractFile struct {
	EventType string `yaml:"event_type"`
	Request   struct {
		Method  string            `yaml:"method"`
		Path    string            `yaml:"path"`
		Headers map[string]string `yaml:"headers"`
		Body    struct {
			Type     string         `yaml:"type"`
			Template map[string]any `yaml:"template"`
		} `yaml:"body"`
	} `yaml:"request"`
	RetryPolicy *vendorRetryFile `yaml:"retry_policy"`
}

type vendorResponseJudgment struct {
	Success   *vendorResponseRuleItem `yaml:"success"`
	Retryable *vendorResponseRuleItem `yaml:"retryable"`
}

type vendorResponseRuleItem struct {
	Type           string `yaml:"type"`
	Field          string `yaml:"field"`
	ExpectedValue  any    `yaml:"expected"`
	ExpectedStatus int    `yaml:"expected_status"`
}

// eventSchemaFile is the YAML representation of an event schema file.
type eventSchemaFile struct {
	EventType   string         `yaml:"event_type"`
	Description string         `yaml:"description"`
	Version     int            `yaml:"version"`
	Schema      map[string]any `yaml:"schema"`
}

// LoadedValue wraps a config value with its load error.
// Value is nil when Error != nil; Error is nil when load succeeded.
// This lets consumers answer "is this item available?" from one lookup
// instead of checking separate error maps.
type LoadedValue[T any] struct {
	Value T
	Error error
}

// ---- Loader ----

// Loader implements port.ConfigProvider by loading configuration from YAML files.
//
// Once loaded, a Loader is immutable — no method modifies its maps after Load
// returns. This is a deliberate design choice for two reasons:
//
//  1. Partial-update consistency: if a future hot-reload mechanism updates
//     maps incrementally (e.g. reloading vendors first, then routing rules),
//     a concurrent Get* call could observe an inconsistent cross-section —
//     e.g. the new route for a vendor whose old delivery contract is still in
//     place. An atomic pointer swap avoids this entirely: a new Loader is
//     fully constructed in the background, then swapped in one atomic store.
//
//  2. Residual config detection: incremental in-place updates must diff
//     file-system state against in-memory state to find deletions. Without a
//     full diff, a config file that was deleted from disk silently remains in
//     memory, and the system runs with stale config forever. A full rebuild
//     from scratch (NewLoader → Load → atomic.Swap) guarantees that the
//     Loader reflects exactly what is on disk — nothing more, nothing less.
//
// Hot-reload pattern:
//
//	newLoader := NewLoader(configDir)
//	if err := newLoader.Load(ctx); err != nil { ... }
//	atomic.StorePointer(&currentLoader, newLoader)
//	// The old Loader is garbage-collected; all new requests see the new config.
type Loader struct {
	paths []string

	routingRules      map[string]*LoadedValue[[]port.RoutingRule] // key: eventType
	vendorConfigs     map[string]*LoadedValue[*port.VendorConfig]
	deliveryContracts map[string]*LoadedValue[*port.DeliverySpec] // key: "vendorID/eventType"
	eventSchemas      map[string]*LoadedValue[map[string]any]     // key: eventType
}

// NewLoader creates a new config loader for the given config file or directory paths.
func NewLoader(paths ...string) (*Loader, error) {
	return &Loader{
		paths:             paths,
		routingRules:      make(map[string]*LoadedValue[[]port.RoutingRule]),
		vendorConfigs:     make(map[string]*LoadedValue[*port.VendorConfig]),
		deliveryContracts: make(map[string]*LoadedValue[*port.DeliverySpec]),
		eventSchemas:      make(map[string]*LoadedValue[map[string]any]),
	}, nil
}

// recordError records a loading failure: stores the error in the appropriate
// LoadedValue map entry, then logs it. It does NOT return the error — the
// loader continues with partial availability.
func (l *Loader) recordError(typ, scope, file string, err error) {
	switch typ {
	case configTypeVendor:
		l.vendorConfigs[scope] = &LoadedValue[*port.VendorConfig]{Error: err}
	case configTypeContract:
		l.deliveryContracts[scope] = &LoadedValue[*port.DeliverySpec]{Error: err}
	case configTypeSchema:
		l.eventSchemas[scope] = &LoadedValue[map[string]any]{Error: err}
	case configTypeRoute:
		l.routingRules[scope] = &LoadedValue[[]port.RoutingRule]{Error: err}
	}

	log.Error().
		Str("module", logModuleConfigLoader).
		Str("event", "load_"+typ+"_error").
		Str("file", file).
		Str("scope", scope).
		Err(err).
		Msg(logMsgConfigFailure)
}


// Load loads all configuration from the configured paths into memory.
func (l *Loader) Load(ctx context.Context) error {
	for _, path := range l.paths {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("config path %q: %w", path, err)
		}

		if info.IsDir() {
			if err := l.loadDir(path); err != nil {
				return err
			}
		} else {
		}
	}

	l.validateCrossConfig()
	return nil
}

// loadDir loads a configuration directory in the DD §4.1 structure:
//
//	config/
//	├── events/
//	│   └── {biz}/
//	│       ├── routes/                        routing rules
//	│       │   └── {event}.yaml
//	│       └── events/
//	│           └── {event}.yaml               event schema
//	└── vendors/
//	    └── {vendor}/
//	        ├── vendor.yaml                    vendor config
//	        └── {biz}/
//	            └── {event}.yaml               delivery contract
func (l *Loader) loadDir(dir string) error {
	eventsDir := filepath.Join(dir, "events")
	vendorsDir := filepath.Join(dir, "vendors")

	eventsExist := existsAndIsDir(eventsDir)
	vendorsExist := existsAndIsDir(vendorsDir)

	if !eventsExist && !vendorsExist {
		return fmt.Errorf("neither %q nor %q exist or are readable", eventsDir, vendorsDir)
	}

	if eventsExist {
		walkYAML[routesFile](eventsDir, "{biz}/routes/{event}.yaml",
			func(file routesFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError(configTypeRoute, vars["event"], path, err)
					return
				}
				if file.EventType == "" {
					l.recordError(configTypeRoute, vars["event"], path, fmt.Errorf("missing event_type"))
					return
				}
				var rules []port.RoutingRule
				for _, item := range file.Routes {
					rules = append(rules, port.RoutingRule{EventType: file.EventType, VendorID: item.VendorID})
				}
				l.routingRules[file.EventType] = &LoadedValue[[]port.RoutingRule]{Value: rules}
			})
		walkYAML[eventSchemaFile](eventsDir, "{biz}/events/{event}.yaml",
			func(sf eventSchemaFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError(configTypeSchema, vars["event"], path, err)
					return
				}
				if sf.EventType == "" || sf.Schema == nil {
					l.recordError(configTypeSchema, vars["event"], path, fmt.Errorf("missing event_type or schema"))
					return
				}
				l.eventSchemas[sf.EventType] = &LoadedValue[map[string]any]{Value: sf.Schema}
			})
	}
	if vendorsExist {
		walkYAML[vendorConfigFile](vendorsDir, "{vendor}/vendor.yaml",
			func(file vendorConfigFile, path string, vars map[string]string, err error) {
				if err != nil {
					vendorID := vars["vendor"]
					l.recordError(configTypeVendor, vendorID, path, err)
					return
				}
				vendorID := vars["vendor"]
				vendorRetry, err := convertVendorRetryFile(&file.RetryPolicy)
				if err != nil {
					l.recordError(configTypeVendor, vendorID, path, fmt.Errorf("retry_policy: %w", err))
					return
				}
				vendor := &port.VendorConfig{
					VendorID: vendorID,
					BaseURL:  file.BaseURL,
					Retry:    *vendorRetry,
					Judgment: convertResponseJudgment(file.ResponseJudgment),
				}
				if file.Auth != nil {
					vendor.Auth = &port.AuthConfig{
						Type:   file.Auth.Type,
						Config: file.Auth.Config,
					}
				}
				l.vendorConfigs[vendorID] = &LoadedValue[*port.VendorConfig]{Value: vendor}
			})
		walkYAML[deliveryContractFile](vendorsDir, "{vendor}/{biz}/{event}.yaml",
			func(file deliveryContractFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError(configTypeContract, vars["vendor"]+"/"+vars["event"], path, err)
					return
				}
				vendorID := vars["vendor"]
				eventType := file.EventType
				if eventType == "" {
					eventType = vars["event"]
				}
				scope := vendorID + "/" + filepath.Base(path)
				spec := &port.DeliverySpec{
					Mapping: port.MappingConfig{
						EventType: eventType,
						Request: port.RequestConfig{
							Method:  file.Request.Method,
							Path:    file.Request.Path,
							Headers: file.Request.Headers,
						},
						Body: port.BodyConfig{
							Type:     file.Request.Body.Type,
							Template: file.Request.Body.Template,
						},
					},
				}
				if file.RetryPolicy != nil {
					retry, err := convertVendorRetryFile(file.RetryPolicy)
					if err != nil {
						l.recordError(configTypeContract, scope, path, fmt.Errorf("contract retry_policy: %w", err))
						return
					}
					spec.Retry = retry
				}
				key := vendorID + "/" + eventType
				l.deliveryContracts[key] = &LoadedValue[*port.DeliverySpec]{Value: spec}
			})
	}
	return nil
}

// ---- Helpers ----

// parseDurationToMs parses a Go duration string and returns milliseconds.
func parseDurationToMs(s string) (int, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return int(d.Milliseconds()), nil
}

// convertVendorRetryFile maps the YAML retry format to the port type.
func convertVendorRetryFile(r *vendorRetryFile) (*port.RetryPolicy, error) {
	if r == nil {
		return nil, nil
	}
	baseDelayMs, err := parseDurationToMs(r.BaseDelay)
	if err != nil {
		return nil, err
	}
	maxDelayMs, err := parseDurationToMs(r.MaxDelay)
	if err != nil {
		return nil, err
	}
	return &port.RetryPolicy{
		MaxAttempts: r.MaxAttempts,
		BaseDelayMs: baseDelayMs,
		MaxDelayMs:  maxDelayMs,
		Multiplier:  r.Multiplier,
		Jitter:      r.Jitter,
	}, nil
}

// convertResponseJudgment maps the YAML judgment format to the port type.
func convertResponseJudgment(rj *vendorResponseJudgment) port.ResponseJudgment {
	if rj == nil {
		return port.ResponseJudgment{}
	}

	result := port.ResponseJudgment{}

	if rj.Success != nil {
		result.SuccessRules = []port.ResponseRule{
			{
				JudgeType:      rj.Success.Type,
				Field:          rj.Success.Field,
				Expected:       rj.Success.ExpectedValue,
				ExpectedStatus: rj.Success.ExpectedStatus,
			},
		}
	}

	if rj.Retryable != nil {
		result.RetryableRules = []port.ResponseRule{
			{
				JudgeType:      rj.Retryable.Type,
				Field:          rj.Retryable.Field,
				Expected:       rj.Retryable.ExpectedValue,
				ExpectedStatus: rj.Retryable.ExpectedStatus,
			},
		}
	}

	return result
}

// existsAndIsDir returns true if path exists and is a directory.
func existsAndIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ---- Template field ref validation ----

// schemaHasPath checks whether a dotted path exists in a schema properties map.
func schemaHasPath(props map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	for i, part := range parts {
		if props == nil {
			return false
		}
		val, ok := props[part]
		if !ok {
			return false
		}
		if i < len(parts)-1 {
			props, ok = val.(map[string]any)
			if !ok {
				return false
			}
		}
	}
	return true
}

// extractRefField extracts the first segment of a @{scope:path} reference.
// For "@{payload:products}" returns "products". For "@{item}" returns "".
func extractRefField(s, prefix string) string {
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	end := strings.IndexByte(s, '}')
	if end < len(prefix)+1 {
		return ""
	}
	field := s[len(prefix):end]
	if dot := strings.IndexByte(field, '.'); dot > 0 {
		field = field[:dot]
	}
	return field
}

// validateString validates @{payload:xxx} and @{item:xxx} in a single string.
func validateString(s string, rootProps map[string]any, itemPath string) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '@' {
			continue
		}
		switch {
		case i+10 <= len(s) && s[i:i+len(refPrefixPayload)] == refPrefixPayload:
			field := extractRefField(s[i:], refPrefixPayload)
			if field != "" && rootProps[field] == nil {
				return fmt.Errorf("references payload field %q not declared in event schema", field)
			}
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				i += end
			}
		case i+7 <= len(s) && s[i:i+len(refPrefixItem)] == refPrefixItem:
			if itemPath == "" {
				return fmt.Errorf("@{item:...} reference used outside $each block")
			}
			field := extractRefField(s[i:], refPrefixItem)
			if field != "" && !schemaHasPath(rootProps, itemPath+"."+field) {
				return fmt.Errorf("references item field %q not declared in array item schema", field)
			}
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				i += end
			}
		case i+6 <= len(s) && s[i:i+len(refItemBare)] == refItemBare:
			if itemPath == "" {
				return fmt.Errorf("@{item} reference used outside $each block")
			}
			i += 5
		}
	}
	return nil
}

// deriveItemPath constructs the items.properties schema path from a $source expression.
func deriveItemPath(sourceExpr, currentItemPath string) string {
	switch {
	case strings.HasPrefix(sourceExpr, refPrefixPayload):
		field := extractRefField(sourceExpr, "@{payload:")
		if field == "" {
			return ""
		}
		return field + ".items.properties"
	case strings.HasPrefix(sourceExpr, refPrefixItem):
		field := extractRefField(sourceExpr, "@{item:")
		if field == "" {
			return ""
		}
		if currentItemPath == "" {
			return field + ".items.properties"
		}
		return currentItemPath + "." + field + ".items.properties"
	case sourceExpr == refItemBare:
		return currentItemPath
	}
	return ""
}

// validateTemplate recursively validates field references in a template node.
func validateTemplate(node any, rootProps map[string]any, itemPath string) error {
	switch v := node.(type) {
	case string:
		return validateString(v, rootProps, itemPath)
	case map[string]any:
		if srcRaw, hasSource := v[port.DirectiveSource]; hasSource {
			if srcStr, ok := srcRaw.(string); ok {
				if err := validateString(srcStr, rootProps, itemPath); err != nil {
					return err
				}
			}
			if _, hasEach := v[port.DirectiveEach]; hasEach {
				var newItemPath string
				if srcStr, ok := srcRaw.(string); ok {
					newItemPath = deriveItemPath(srcStr, itemPath)
				}
				return validateTemplate(v[port.DirectiveEach], rootProps, newItemPath)
			}
			return nil
		}
		for _, val := range v {
			if err := validateTemplate(val, rootProps, itemPath); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, elem := range v {
			if err := validateTemplate(elem, rootProps, itemPath); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}
// runtime lookups return the error. Startup is NOT blocked.
func (l *Loader) validateCrossConfig() {

	// 1. Delivery contract template fields exist in event schema
	for key, lv := range l.deliveryContracts {
		if lv.Error != nil || lv.Value == nil {
			continue
		}
		spec := lv.Value
		eventType := spec.Mapping.EventType
		if eventType == "" {
			continue
		}

		schemaLV, hasSchema := l.eventSchemas[eventType]
		if !hasSchema || schemaLV.Error != nil || schemaLV.Value == nil {
			continue
		}

		schemaMap := schemaLV.Value
		propsRaw, _ := schemaMap[schemaKeyProperties]
		props, ok := propsRaw.(map[string]any)
		if !ok || props == nil {
			continue
		}

		// Validate template field references in body, path, and headers
		// against event schema, including @{item:...} inside $each blocks.
		// All three locations share the same props context (no $each here,
		// so itemProps is nil). A failure in any one marks the contract
		// unavailable.
		nodes := []any{spec.Mapping.Body.Template, spec.Mapping.Request.Path}
		for _, h := range spec.Mapping.Request.Headers {
			nodes = append(nodes, h)
		}
		for _, node := range nodes {
			if err := validateTemplate(node, props, ""); err != nil {
				l.deliveryContracts[key] = &LoadedValue[*port.DeliverySpec]{
					Error: fmt.Errorf("contract %s", err),
				}
				break
			}
		}
	}
}

// ---- Generic helpers ----

// lookup returns the value for key from a LoadedValue map, or ErrNotConfigured.
func lookup[T any](m map[string]*LoadedValue[T], key string) (T, error) {
	lv, ok := m[key]
	if !ok {
		var zero T
		return zero, port.ErrNotConfigured
	}
	if lv.Error != nil {
		var zero T
		return zero, lv.Error
	}
	return lv.Value, nil
}

// ---- ConfigProvider implementation ----

// GetVendorConfig returns the vendor configuration for the given vendor ID.
func (l *Loader) GetVendorConfig(vendorID string) (*port.VendorConfig, error) {
	return lookup(l.vendorConfigs, vendorID)
}

// GetAllVendorIDs returns all known vendor IDs.
func (l *Loader) GetAllVendorIDs() []string {

	ids := make([]string, 0, len(l.vendorConfigs))
	for id := range l.vendorConfigs {
		ids = append(ids, id)
	}
	return ids
}

// GetDeliverySpec returns the delivery specification for the given vendor and event type.
// The delivery contract is the required source for the API call parameters (method, path,
// headers, body). The vendor config provides base_url, auth, default retry, and default
// judgment. The contract may optionally override the retry policy.
func (l *Loader) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, error) {

	vendorLV, ok := l.vendorConfigs[vendorID]
	if !ok {
		return nil, port.ErrNotConfigured
	}
	if vendorLV.Error != nil {
		return nil, vendorLV.Error
	}
	vendor := vendorLV.Value

	contractKey := vendorID + "/" + eventType
	contractLV, hasContract := l.deliveryContracts[contractKey]
	if !hasContract {
		return nil, port.ErrNotConfigured
	}
	if contractLV.Error != nil {
		return nil, contractLV.Error
	}
	specLV := contractLV.Value

	// Shallow-copy the stored spec, then fill in vendor-level defaults.
	spec := *specLV
	if spec.Retry == nil {
		retry := vendor.Retry
		spec.Retry = &retry
	}

	return &spec, nil
}

// GetRoutingRules returns all routing rules matching the given event type.
func (l *Loader) GetRoutingRules(eventType string) ([]port.RoutingRule, error) {
	return lookup(l.routingRules, eventType)
}

// GetEventSchema returns the event schema for the given event type.
func (l *Loader) GetEventSchema(eventType string) (map[string]any, error) {
	return lookup(l.eventSchemas, eventType)
}
