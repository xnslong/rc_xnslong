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
// loader continues with partial availability.
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
			l.loadFile(path)
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
	vendorsDir := filepath.Join(dir, "vendors")

	eventsExist := existsAndIsDir(eventsDir)
	vendorsExist := existsAndIsDir(vendorsDir)

	if !eventsExist && !vendorsExist {
		return fmt.Errorf("neither %q nor %q exist or are readable", eventsDir, vendorsDir)
	}

	if eventsExist {
		l.loadRoutesFromEventsDir(eventsDir)
		l.loadHierarchicalEventSchemas(eventsDir)
	}
	if vendorsExist {
		l.loadVendorsDir(vendorsDir)
	}

	return nil
}

// loadFile loads a single YAML file, determining its type from the filename.
func (l *Loader) loadFile(path string) error {
	base := filepath.Base(path)
	switch base {
	case "vendor.yaml":
		l.loadVendorConfig(path)
		return nil
	default:
		return fmt.Errorf("unknown config file type: %s", base)
	}
}

// ---- Routing rules ----

// loadRoutesFromEventsDir scans events/{biz}/routes/*.yaml for each biz.
func (l *Loader) loadRoutesFromEventsDir(eventsDir string) {
	bizEntries, err := os.ReadDir(eventsDir)
	if err != nil {
		l.recordError("route", "events", eventsDir, fmt.Errorf("reading events dir: %w", err))
		return
	}

	for _, bizEntry := range bizEntries {
		if !bizEntry.IsDir() {
			continue
		}

		routesDir := filepath.Join(eventsDir, bizEntry.Name(), "routes")
		if !existsAndIsDir(routesDir) {
			continue
		}

		routeEntries, err := os.ReadDir(routesDir)
		if err != nil {
			l.recordError("route", "biz:"+bizEntry.Name(), routesDir,
				fmt.Errorf("reading routes dir: %w", err))
			continue
		}

		for _, re := range routeEntries {
			if re.IsDir() || !strings.HasSuffix(re.Name(), ".yaml") {
				continue
			}
			l.loadBizRoute(filepath.Join(routesDir, re.Name()))
		}
	}
}

// loadBizRoute parses an events/{biz}/routes/*.yaml file.
func (l *Loader) loadBizRoute(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		l.recordError("route", path, path, fmt.Errorf("reading file: %w", err))
		return
	}

	var file routesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		l.recordError("route", path, path, fmt.Errorf("parsing YAML: %w", err))
		return
	}

	if file.EventType == "" {
		l.recordError("route", path, path, fmt.Errorf("missing event_type"))
		return
	}

	var rules []*port.RoutingRule
	for _, item := range file.Routes {
		rules = append(rules, &port.RoutingRule{
			EventType: file.EventType,
			VendorID:  item.VendorID,
		})
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.routingRules[file.EventType] = &LoadedValue[[]*port.RoutingRule]{Value: rules}
}

// ---- Vendor configs ----

// loadVendorsDir scans vendors/{vendor}/vendor.yaml and delivery contracts.
func (l *Loader) loadVendorsDir(vendorsDir string) {
	vendorEntries, err := os.ReadDir(vendorsDir)
	if err != nil {
		l.recordError("vendor", "vendors", vendorsDir,
			fmt.Errorf("reading vendors dir: %w", err))
		return
	}

	for _, entry := range vendorEntries {
		if !entry.IsDir() {
			continue
		}
		vendorID := entry.Name()
		vendorDir := filepath.Join(vendorsDir, vendorID)

		vendorPath := filepath.Join(vendorDir, "vendor.yaml")
		if !fileExists(vendorPath) {
			l.recordError("vendor", vendorID, vendorPath,
				fmt.Errorf("vendor.yaml not found or is a directory"))
		} else {
			l.loadVendorConfig(vendorPath)
		}

		l.loadDeliveryContractsForVendor(vendorDir, vendorID)
	}
}

// loadVendorConfig parses a vendor YAML file and stores the result.
func (l *Loader) loadVendorConfig(path string) {
	// The immediate parent directory name is the vendor_id.
	vendorID := filepath.Base(filepath.Dir(path))

	data, err := os.ReadFile(path)
	if err != nil {
		l.recordError("vendor", vendorID, path,
			fmt.Errorf("reading file: %w", err))
		return
	}

	var file vendorConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		l.recordError("vendor", vendorID, path,
			fmt.Errorf("parsing YAML: %w", err))
		return
	}

	baseDelayMs, err := parseDurationToMs(file.RetryPolicy.BaseDelay)
	if err != nil {
		l.recordError("vendor", file.VendorID, path,
			fmt.Errorf("parsing base_delay: %w", err))
		return
	}
	maxDelayMs, err := parseDurationToMs(file.RetryPolicy.MaxDelay)
	if err != nil {
		l.recordError("vendor", file.VendorID, path,
			fmt.Errorf("parsing max_delay: %w", err))
		return
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
}

// ---- Delivery contracts ----

// loadDeliveryContractsForVendor scans vendors/{vendor}/{biz}/*.yaml for delivery contracts.
func (l *Loader) loadDeliveryContractsForVendor(vendorDir, vendorID string) {
	entries, err := os.ReadDir(vendorDir)
	if err != nil {
		l.recordError("contract", vendorID, vendorDir,
			fmt.Errorf("reading vendor dir: %w", err))
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "vendor.yaml" {
			continue
		}

		bizDir := filepath.Join(vendorDir, entry.Name())
		bizEntries, err := os.ReadDir(bizDir)
		if err != nil {
			l.recordError("contract", vendorID+"/"+entry.Name(), bizDir,
				fmt.Errorf("reading biz dir: %w", err))
			continue
		}

		for _, fe := range bizEntries {
			if fe.IsDir() || filepath.Ext(fe.Name()) != ".yaml" {
				continue
			}
			l.loadDeliveryContract(filepath.Join(bizDir, fe.Name()), vendorID)
		}
	}
}

// loadDeliveryContract parses a single delivery contract YAML file and stores it.
func (l *Loader) loadDeliveryContract(path, vendorID string) {
	data, err := os.ReadFile(path)
	if err != nil {
		l.recordError("contract", vendorID+"/"+filepath.Base(path), path,
			fmt.Errorf("reading file: %w", err))
		return
	}

	var file deliveryContractFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		l.recordError("contract", vendorID+"/"+filepath.Base(path), path,
			fmt.Errorf("parsing YAML: %w", err))
		return
	}

	eventType := file.EventType
	if eventType == "" {
		eventType = strings.TrimSuffix(filepath.Base(path), ".yaml")
	}

	key := vendorID + "/" + eventType

	l.mu.Lock()
	defer l.mu.Unlock()
	l.deliveryContracts[key] = &LoadedValue[*deliveryContractFile]{Value: &file}
}

// ---- Event schemas ----

// loadHierarchicalEventSchemas loads schema YAML files from events/{biz}/events/.
func (l *Loader) loadHierarchicalEventSchemas(dir string) {
	bizEntries, err := os.ReadDir(dir)
	if err != nil {
		l.recordError("schema", dir, dir, fmt.Errorf("reading events dir: %w", err))
		return
	}

	for _, bizEntry := range bizEntries {
		if !bizEntry.IsDir() {
			continue
		}

		eventsDir := filepath.Join(dir, bizEntry.Name(), "events")
		if !existsAndIsDir(eventsDir) {
			continue
		}

		schemaEntries, err := os.ReadDir(eventsDir)
		if err != nil {
			l.recordError("schema", bizEntry.Name(), eventsDir,
				fmt.Errorf("reading schema dir: %w", err))
			continue
		}

		for _, entry := range schemaEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			l.loadEventSchemaFile(filepath.Join(eventsDir, entry.Name()))
		}
	}
}

// loadEventSchemaFile parses a single event schema YAML file and stores it.
func (l *Loader) loadEventSchemaFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		l.recordError("schema", path, path, fmt.Errorf("reading file: %w", err))
		return
	}

	var sf eventSchemaFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		l.recordError("schema", path, path, fmt.Errorf("parsing YAML: %w", err))
		return
	}

	if sf.EventType == "" || sf.Schema == nil {
		l.recordError("schema", path, path, fmt.Errorf("missing event_type or schema"))
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// Store the parsed map directly — no json.Marshal round-trip.
	l.eventSchemas[sf.EventType] = &LoadedValue[map[string]any]{Value: sf.Schema}
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
				l.routingRules[eventType] = &LoadedValue[[]*port.RoutingRule]{
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
		contract := lv.Value
		eventType := contract.EventType
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
		refs := extractPayloadRefs(contract.Request.Body.Template)
		for _, ref := range refs {
			if _, exists := props[ref]; !exists {
				l.deliveryContracts[key] = &LoadedValue[*deliveryContractFile]{
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
	if lv.Error != nil {
		return nil, lv.Error
	}
	// Convert []*port.RoutingRule → []port.RoutingRule
	rules := make([]port.RoutingRule, len(lv.Value))
	for i, r := range lv.Value {
		rules[i] = *r
	}
	return rules, nil
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
