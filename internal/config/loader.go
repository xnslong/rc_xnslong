package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rs/zerolog/log"
	"github.com/xnslong/rc_xnslong/internal/port"
)

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
// MVP uses local file loading; future stages will support Git + Webhook + SecretStore.
type Loader struct {
	mu    sync.RWMutex
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
	l.mu.Lock()
	switch typ {
	case "vendor":
		l.vendorConfigs[scope] = &LoadedValue[*port.VendorConfig]{Error: err}
	case "contract":
		l.deliveryContracts[scope] = &LoadedValue[*port.DeliverySpec]{Error: err}
	case "schema":
		l.eventSchemas[scope] = &LoadedValue[map[string]any]{Error: err}
	case "route":
		l.routingRules[scope] = &LoadedValue[[]port.RoutingRule]{Error: err}
	}
	l.mu.Unlock()

	log.Error().
		Str("module", "config.loader").
		Str("event", "load_"+typ+"_error").
		Str("file", file).
		Str("scope", scope).
		Err(err).
		Msg("config load failure")
}

// ---- YAML template walker ----

// yamlSeg is a parsed segment of a path template.
type yamlSeg struct {
	isVar   bool
	name    string
	fileExt string
}

// parseYAMLTemplate parses a template like "{biz}/routes/{event}.yaml".
func parseYAMLTemplate(tmpl string) []yamlSeg {
	parts := strings.Split(tmpl, "/")
	segs := make([]yamlSeg, len(parts))
	for i, part := range parts {
		if brace := strings.IndexByte(part, '{'); brace >= 0 {
			closeB := strings.IndexByte(part, '}')
			segs[i] = yamlSeg{isVar: true, name: part[brace+1 : closeB], fileExt: part[closeB+1:]}
		} else {
			segs[i] = yamlSeg{isVar: false, name: part}
		}
	}
	return segs
}

// walkYAML walks a path template relative to rootDir, finds all matching
// .yaml files, parses each into T, and calls fn for each.
func walkYAML[T any](l *Loader, rootDir, tmpl, typ string, fn func(T, string, map[string]string, error)) {
	segs := parseYAMLTemplate(tmpl)
	walkYAMLAt[T](l, rootDir, segs, 0, typ, map[string]string{}, fn)
}

func walkYAMLAt[T any](l *Loader, dir string, segs []yamlSeg, idx int, typ string,
	vars map[string]string, fn func(T, string, map[string]string, error)) {
	if idx >= len(segs) {
		return
	}
	seg := segs[idx]
	isLast := idx == len(segs)-1

	if isLast {
		if seg.isVar {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), seg.fileExt) {
					continue
				}
				v := copyMap(vars)
				v[seg.name] = strings.TrimSuffix(e.Name(), seg.fileExt)
				parseYAMLFileAt[T](l, filepath.Join(dir, e.Name()), typ, v, fn)
			}
		} else {
			parseYAMLFileAt[T](l, filepath.Join(dir, seg.name), typ, vars, fn)
		}
		return
	}

	if seg.isVar {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := copyMap(vars)
			v[seg.name] = e.Name()
			walkYAMLAt[T](l, filepath.Join(dir, e.Name()), segs, idx+1, typ, v, fn)
		}
	} else {
		subDir := filepath.Join(dir, seg.name)
		if !existsAndIsDir(subDir) {
			return
		}
		walkYAMLAt[T](l, subDir, segs, idx+1, typ, vars, fn)
	}
}

func parseYAMLFileAt[T any](l *Loader, path, typ string, vars map[string]string, fn func(T, string, map[string]string, error)) {
	var val T
	data, err := os.ReadFile(path)
	if err != nil {
		var zero T
		fn(zero, path, vars, fmt.Errorf("reading file: %w", err))
		return
	}
	if err := yaml.Unmarshal(data, &val); err != nil {
		var zero T
		fn(zero, path, vars, fmt.Errorf("parsing YAML: %w", err))
		return
	}
	fn(val, path, vars, nil)
}

func copyMap(m map[string]string) map[string]string {
	r := make(map[string]string, len(m))
	for k, v := range m {
		r[k] = v
	}
	return r
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
		walkYAML[routesFile](l, eventsDir, "{biz}/routes/{event}.yaml", "route",
			func(file routesFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError("route", path, path, err)
					return
				}
				if file.EventType == "" {
					l.recordError("route", path, path, fmt.Errorf("missing event_type"))
					return
				}
				var rules []port.RoutingRule
				for _, item := range file.Routes {
					rules = append(rules, port.RoutingRule{EventType: file.EventType, VendorID: item.VendorID})
				}
				l.routingRules[file.EventType] = &LoadedValue[[]port.RoutingRule]{Value: rules}
			})
		walkYAML[eventSchemaFile](l, eventsDir, "{biz}/events/{event}.yaml", "schema",
			func(sf eventSchemaFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError("schema", path, path, err)
					return
				}
				if sf.EventType == "" || sf.Schema == nil {
					l.recordError("schema", path, path, fmt.Errorf("missing event_type or schema"))
					return
				}
				l.eventSchemas[sf.EventType] = &LoadedValue[map[string]any]{Value: sf.Schema}
			})
	}
	if vendorsExist {
		walkYAML[vendorConfigFile](l, vendorsDir, "{vendor}/vendor.yaml", "vendor",
			func(file vendorConfigFile, path string, vars map[string]string, err error) {
				if err != nil {
					vendorID := vars["vendor"]
					l.recordError("vendor", vendorID, path, err)
					return
				}
				vendorID := vars["vendor"]
				baseDelayMs, err := parseDurationToMs(file.RetryPolicy.BaseDelay)
				if err != nil {
					l.recordError("vendor", vendorID, path, fmt.Errorf("parsing base_delay: %w", err))
					return
				}
				maxDelayMs, err := parseDurationToMs(file.RetryPolicy.MaxDelay)
				if err != nil {
					l.recordError("vendor", vendorID, path, fmt.Errorf("parsing max_delay: %w", err))
					return
				}
				vendor := &port.VendorConfig{
					VendorID: vendorID,
					BaseURL:  file.BaseURL,
					Retry: port.RetryPolicy{
						MaxAttempts: file.RetryPolicy.MaxAttempts,
						BaseDelayMs: baseDelayMs,
						MaxDelayMs:  maxDelayMs,
						Multiplier:  file.RetryPolicy.Multiplier,
						Jitter:      file.RetryPolicy.Jitter,
					},
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
		walkYAML[deliveryContractFile](l, vendorsDir, "{vendor}/{biz}/{event}.yaml", "contract",
			func(file deliveryContractFile, path string, vars map[string]string, err error) {
				if err != nil {
					l.recordError("contract", vars["vendor"]+"/"+filepath.Base(path), path, err)
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
						l.recordError("contract", scope, path, fmt.Errorf("contract retry_policy: %w", err))
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

// extractPayloadRefs extracts all @{payload:...} field references from a template value.
// It recursively walks maps and arrays to find all string values containing payload references.
func extractPayloadRefs(val any) []string {
	var refs []string
	extractPayloadRefsRecursive(val, &refs)
	return refs
}

func extractPayloadRefsRecursive(val any, refs *[]string) {
	switch v := val.(type) {
	case string:
		// Find all @{payload:...} occurrences in the string
		for i := 0; i < len(v); i++ {
			if v[i] == '@' && i+10 < len(v) && v[i:i+10] == "@{payload:" {
				end := strings.Index(v[i:], "}")
				if end > 0 {
					ref := v[i+10 : i+end]
					// Only take the first segment for nested paths (e.g. "a.b.c" -> "a")
					if dot := strings.IndexByte(ref, '.'); dot > 0 {
						ref = ref[:dot]
					}
					if ref != "" {
						*refs = append(*refs, ref)
					}
					i += end
				}
			}
		}
	case map[string]any:
		for _, child := range v {
			extractPayloadRefsRecursive(child, refs)
		}
	case []any:
		for _, child := range v {
			extractPayloadRefsRecursive(child, refs)
		}
	}
}

// validateCrossConfig checks for consistency between independently-loaded
// config items. Failures mark the corresponding entry as errored so that
// runtime lookups return the error. Startup is NOT blocked.
func (l *Loader) validateCrossConfig() {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. Routing rules reference existing vendors
	for eventType, lv := range l.routingRules {
		if lv.Error != nil || lv.Value == nil {
			continue
		}
		for _, rule := range lv.Value {
			if _, ok := l.vendorConfigs[rule.VendorID]; !ok {
				l.routingRules[eventType] = &LoadedValue[[]port.RoutingRule]{
					Error: fmt.Errorf("routing rule references non-existent vendor %q", rule.VendorID),
				}
				break
			}
		}
	}

	// 2. Delivery contract template fields exist in event schema
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
		propsRaw, _ := schemaMap["properties"]
		props, ok := propsRaw.(map[string]any)
		if !ok || props == nil {
			continue
		}

		// Extract template field references
		refs := extractPayloadRefs(spec.Mapping.Body.Template)
		for _, ref := range refs {
			if _, exists := props[ref]; !exists {
				l.deliveryContracts[key] = &LoadedValue[*port.DeliverySpec]{
					Error: fmt.Errorf("contract references field %q not declared in schema for %q", ref, eventType),
				}
				break
			}
		}
	}
}

// ---- ConfigProvider implementation ----

// GetVendorConfig returns the vendor configuration for the given vendor ID.
func (l *Loader) GetVendorConfig(vendorID string) (*port.VendorConfig, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	lv, ok := l.vendorConfigs[vendorID]
	if !ok {
		return nil, port.ErrNotConfigured
	}
	return lv.Value, lv.Error
}

// GetAllVendorIDs returns all known vendor IDs.
func (l *Loader) GetAllVendorIDs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

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
	l.mu.RLock()
	defer l.mu.RUnlock()

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
	l.mu.RLock()
	defer l.mu.RUnlock()

	lv, ok := l.routingRules[eventType]
	if !ok {
		return nil, port.ErrNotConfigured
	}
	if lv.Error != nil {
		return nil, lv.Error
	}
	return lv.Value, lv.Error
}

// GetEventSchema returns the event schema for the given event type.
func (l *Loader) GetEventSchema(eventType string) (map[string]any, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	lv, ok := l.eventSchemas[eventType]
	if !ok {
		return nil, port.ErrNotConfigured
	}
	return lv.Value, lv.Error
}
