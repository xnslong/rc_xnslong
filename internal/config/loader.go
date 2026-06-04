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

// routingRuleItem is a single event_type → vendor_id mapping.
type routingRuleItem struct {
	EventType string `yaml:"event_type"`
	VendorID  string `yaml:"vendor_id"`
}

// routesFile is the YAML representation of events/{biz}/routes/{event}.yaml.
type routesFile struct {
	EventType string `yaml:"event_type"`
	Routes    []struct {
		VendorID string `yaml:"vendor_id"`
	} `yaml:"routes"`
}

// vendorConfigFile is the YAML representation of a vendor config file
// located at vendors/{vendor}/vendor.yaml.
// body.template is NOT in the vendor YAML — it lives in the delivery contract
// at vendors/{vendor}/{biz}/{event}.yaml.
type vendorConfigFile struct {
	VendorID string `yaml:"vendor_id"`
	Request  struct {
		Method  string            `yaml:"method"`
		URL     string            `yaml:"url"`
		Headers map[string]string `yaml:"headers"`
		Body    struct {
			Type string `yaml:"type"`
		} `yaml:"body"`
	} `yaml:"request"`
	RetryPolicy struct {
		MaxAttempts int     `yaml:"max_attempts"`
		BaseDelay   string  `yaml:"base_delay"`
		MaxDelay    string  `yaml:"max_delay"`
		Multiplier  float64 `yaml:"multiplier"`
		Jitter      float64 `yaml:"jitter"`
	} `yaml:"retry_policy"`
	ResponseJudgment *vendorResponseJudgment `yaml:"response_judgment"`
}

// deliveryContractFile is the YAML representation of a delivery contract
// located at vendors/{vendor}/{biz}/{event}.yaml.
// Fields are optional overrides — empty fields inherit from vendor config.
type deliveryContractFile struct {
	EventType string `yaml:"event_type"`
	Request   struct {
		Method  string            `yaml:"method"`
		URL     string            `yaml:"url"`
		Headers map[string]string `yaml:"headers"`
		Body    struct {
			Type     string         `yaml:"type"`
			Template map[string]any `yaml:"template"`
		} `yaml:"body"`
	} `yaml:"request"`
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

	routingRules      map[string]*LoadedValue[[]*port.RoutingRule] // key: eventType
	vendorConfigs     map[string]*LoadedValue[*port.VendorConfig]
	deliveryContracts map[string]*LoadedValue[*deliveryContractFile] // key: "vendorID/eventType"
	eventSchemas      map[string]*LoadedValue[map[string]any]        // key: eventType
}

// NewLoader creates a new config loader for the given config file or directory paths.
func NewLoader(paths ...string) (*Loader, error) {
	return &Loader{
		paths:             paths,
		routingRules:      make(map[string]*LoadedValue[[]*port.RoutingRule]),
		vendorConfigs:     make(map[string]*LoadedValue[*port.VendorConfig]),
		deliveryContracts: make(map[string]*LoadedValue[*deliveryContractFile]),
		eventSchemas:      make(map[string]*LoadedValue[map[string]any]),
	}, nil
}

// recordError records a loading failure: stores the error in the appropriate
// LoadedValue map entry, then logs it. It does NOT return the error — the
// loader continues with degraded operation.
func (l *Loader) recordError(typ, scope, file string, err error) {
	l.mu.Lock()
	switch typ {
	case "vendor":
		l.vendorConfigs[scope] = &LoadedValue[*port.VendorConfig]{Error: err}
	case "contract":
		l.deliveryContracts[scope] = &LoadedValue[*deliveryContractFile]{Error: err}
	case "schema":
		l.eventSchemas[scope] = &LoadedValue[map[string]any]{Error: err}
	case "route":
		l.routingRules[scope] = &LoadedValue[[]*port.RoutingRule]{Error: err}
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
			if err := l.loadFile(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadDir loads a configuration directory in the DD §4.1 structure:
//
//	config/
//	├── events/
//	│   └── {biz}/
//	│       ├── route.yaml                     routing rules
//	│       └── events/
//	│           └── {event}.yaml                event schema
//	└── vendors/
//	    └── {vendor}/
//	        ├── vendor.yaml                    vendor config
//	        └── {biz}/
//	            └── {event}.yaml               delivery contract
func (l *Loader) loadDir(dir string) error {
	eventsDir := filepath.Join(dir, "events")
	if info, err := os.Stat(eventsDir); err == nil && info.IsDir() {
		if err := l.loadRoutesFromEventsDir(eventsDir); err != nil {
			return err
		}
		if err := l.loadHierarchicalEventSchemas(eventsDir); err != nil {
			return err
		}
	}

	vendorsDir := filepath.Join(dir, "vendors")
	if info, err := os.Stat(vendorsDir); err == nil && info.IsDir() {
		if err := l.loadVendorsDir(vendorsDir); err != nil {
			return err
		}
	}

	return nil
}

// loadFile loads a single YAML file, determining its type from the filename.
func (l *Loader) loadFile(path string) error {
	base := filepath.Base(path)
	switch base {
	case "route.yaml":
		return l.loadBizRoute(path)
	case "vendor.yaml":
		return l.loadVendorConfig(path)
	default:
		return fmt.Errorf("unknown config file type: %s", base)
	}
}

// ---- Routing rules ----

// loadRoutesFromEventsDir scans events/{biz}/route.yaml for each biz.
func (l *Loader) loadRoutesFromEventsDir(eventsDir string) error {
	bizEntries, err := os.ReadDir(eventsDir)
	if err != nil {
		return fmt.Errorf("reading events dir %q: %w", eventsDir, err)
	}

	for _, bizEntry := range bizEntries {
		if !bizEntry.IsDir() {
			continue
		}

		routePath := filepath.Join(eventsDir, bizEntry.Name(), "route.yaml")
		if info, err := os.Stat(routePath); err == nil && !info.IsDir() {
			if err := l.loadBizRoute(routePath); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadBizRoute parses an events/{biz}/route.yaml file.
func (l *Loader) loadBizRoute(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var file routesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	rules := make([]*port.RoutingRule, len(file.Routes))
	for i, route := range file.Routes {
		rules[i] = &port.RoutingRule{
			EventType: file.EventType,
			VendorID:  route.VendorID,
		}
	}

	l.routingRules[file.EventType] = &LoadedValue[[]*port.RoutingRule]{Value: rules}

	return nil
}

// ---- Vendor configs ----

// loadVendorsDir scans vendors/{vendor}/vendor.yaml and delivery contracts.
func (l *Loader) loadVendorsDir(vendorsDir string) error {
	vendorEntries, err := os.ReadDir(vendorsDir)
	if err != nil {
		return fmt.Errorf("reading vendors dir %q: %w", vendorsDir, err)
	}

	for _, entry := range vendorEntries {
		if !entry.IsDir() {
			continue
		}
		vendorID := entry.Name()
		vendorDir := filepath.Join(vendorsDir, vendorID)

		// Load vendor.yaml
		vendorPath := filepath.Join(vendorDir, "vendor.yaml")
		if info, err := os.Stat(vendorPath); err == nil && !info.IsDir() {
			if err := l.loadVendorConfig(vendorPath); err != nil {
				return err
			}
		}

		// Load delivery contracts: vendors/{vendorID}/{biz}/*.yaml
		if err := l.loadDeliveryContractsForVendor(vendorDir, vendorID); err != nil {
			return err
		}
	}
	return nil
}

// loadVendorConfig parses a vendor YAML file and stores the result.
func (l *Loader) loadVendorConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var file vendorConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	// Parse duration strings (e.g. "10s") to milliseconds.
	baseDelayMs, err := parseDurationToMs(file.RetryPolicy.BaseDelay)
	if err != nil {
		return fmt.Errorf("parsing base_delay in %q: %w", path, err)
	}
	maxDelayMs, err := parseDurationToMs(file.RetryPolicy.MaxDelay)
	if err != nil {
		return fmt.Errorf("parsing max_delay in %q: %w", path, err)
	}

	vendor := &port.VendorConfig{
		VendorID: file.VendorID,
		Request: port.RequestConfig{
			Method:  file.Request.Method,
			URLTmpl: file.Request.URL,
			Headers: file.Request.Headers,
		},
		Body: port.BodyConfig{
			Type: file.Request.Body.Type,
		},
		Retry: port.RetryPolicy{
			MaxAttempts: file.RetryPolicy.MaxAttempts,
			BaseDelayMs: baseDelayMs,
			MaxDelayMs:  maxDelayMs,
			Multiplier:  file.RetryPolicy.Multiplier,
			Jitter:      file.RetryPolicy.Jitter,
		},
		Judgment: convertResponseJudgment(file.ResponseJudgment),
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.vendorConfigs[file.VendorID] = &LoadedValue[*port.VendorConfig]{Value: vendor}

	return nil
}

// ---- Delivery contracts ----

// loadDeliveryContractsForVendor scans vendors/{vendor}/{biz}/*.yaml for delivery contracts.
func (l *Loader) loadDeliveryContractsForVendor(vendorDir, vendorID string) error {
	entries, err := os.ReadDir(vendorDir)
	if err != nil {
		return fmt.Errorf("reading vendor dir %q: %w", vendorDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "vendor.yaml" {
			continue
		}

		bizDir := filepath.Join(vendorDir, entry.Name())
		bizEntries, err := os.ReadDir(bizDir)
		if err != nil {
			return fmt.Errorf("reading biz dir %q: %w", bizDir, err)
		}

		for _, fe := range bizEntries {
			if fe.IsDir() || filepath.Ext(fe.Name()) != ".yaml" {
				continue
			}
			contractPath := filepath.Join(bizDir, fe.Name())
			if err := l.loadDeliveryContract(contractPath, vendorID); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadDeliveryContract parses a single delivery contract YAML file and stores it.
func (l *Loader) loadDeliveryContract(path, vendorID string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var file deliveryContractFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	eventType := file.EventType
	if eventType == "" {
		eventType = strings.TrimSuffix(filepath.Base(path), ".yaml")
	}

	key := vendorID + "/" + eventType

	l.mu.Lock()
	defer l.mu.Unlock()

	l.deliveryContracts[key] = &LoadedValue[*deliveryContractFile]{Value: &file}

	return nil
}

// ---- Event schemas ----

// loadHierarchicalEventSchemas loads schema YAML files from events/{biz}/events/.
func (l *Loader) loadHierarchicalEventSchemas(dir string) error {
	bizEntries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading events dir %q: %w", dir, err)
	}

	for _, bizEntry := range bizEntries {
		if !bizEntry.IsDir() {
			continue
		}

		eventsDir := filepath.Join(dir, bizEntry.Name(), "events")
		if info, err := os.Stat(eventsDir); err != nil || !info.IsDir() {
			continue
		}

		schemaEntries, err := os.ReadDir(eventsDir)
		if err != nil {
			return fmt.Errorf("reading %s: %w", eventsDir, err)
		}

		for _, entry := range schemaEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}

			path := filepath.Join(eventsDir, entry.Name())
			if err := l.loadEventSchemaFile(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadEventSchemaFile parses a single event schema YAML file and stores it.
func (l *Loader) loadEventSchemaFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var sf eventSchemaFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parsing schema file %q: %w", path, err)
	}

	if sf.EventType == "" || sf.Schema == nil {
		return fmt.Errorf("invalid schema file %q: missing event_type or schema", path)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.eventSchemas[sf.EventType] = &LoadedValue[map[string]any]{Value: sf.Schema}

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
// Merges vendor request config with per-event delivery contract overrides.
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

	spec := &port.DeliverySpec{
		Mapping: port.MappingConfig{
			EventType: eventType,
			Request:   vendor.Request,
		},
	}

	contractKey := vendorID + "/" + eventType
	if contractLV, hasContract := l.deliveryContracts[contractKey]; hasContract {
		if contractLV.Error != nil {
			return nil, contractLV.Error
		}
		contract := contractLV.Value
		if contract.Request.Method != "" {
			spec.Mapping.Request.Method = contract.Request.Method
		}
		if contract.Request.URL != "" {
			spec.Mapping.Request.URLTmpl = contract.Request.URL
		}
		if contract.Request.Headers != nil {
			spec.Mapping.Request.Headers = contract.Request.Headers
		}
		if contract.Request.Body.Template != nil {
			spec.Mapping.Body = port.BodyConfig{
				Type:     contract.Request.Body.Type,
				Template: contract.Request.Body.Template,
			}
		} else {
			spec.Mapping.Body = vendor.Body
		}
	} else {
		spec.Mapping.Body = vendor.Body
	}

	return spec, nil
}

// GetRoutingRules returns all routing rules matching the given event type.
func (l *Loader) GetRoutingRules(eventType string) ([]port.RoutingRule, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	lv, ok := l.routingRules[eventType]
	if !ok {
		return nil, port.ErrNotConfigured
	}
	// Convert []*port.RoutingRule → []port.RoutingRule
	rules := make([]port.RoutingRule, len(lv.Value))
	for i, r := range lv.Value {
		rules[i] = *r
	}
	return rules, lv.Error
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
