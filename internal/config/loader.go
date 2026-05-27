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

	"github.com/xnslong/rc_xnslong/internal/port"
)

// ---- YAML intermediate types ----

// routingRulesFile is the YAML representation of routing_rules.yaml.
type routingRulesFile struct {
	Rules []routingRuleItem `yaml:"rules"`
}

type routingRuleItem struct {
	EventType string `yaml:"event_type"`
	VendorID  string `yaml:"vendor_id"`
}

// vendorConfigFile is the YAML representation of a vendor config file.
// body.template is NOT in the vendor YAML — it lives in mappings/{vid}/{event}.yaml.
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

// mappingConfigFile is the YAML representation of a mapping file.
type mappingConfigFile struct {
	EventType string `yaml:"event_type"`
	Request   struct {
		Body struct {
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

// ---- Loader ----

// Loader implements port.ConfigProvider by loading configuration from YAML files.
// MVP uses local file loading; future stages will support Git + Webhook + SecretStore.
type Loader struct {
	mu    sync.RWMutex
	paths []string

	// loaded state — populated by Load()
	routingRules   []port.RoutingRule
	vendorConfigs  map[string]*port.VendorConfig
	mappingConfigs map[string]*port.MappingConfig // key: "vendorID/eventType"
}

// NewLoader creates a new config loader for the given config file or directory paths.
func NewLoader(paths ...string) (*Loader, error) {
	return &Loader{
		paths:          paths,
		vendorConfigs:  make(map[string]*port.VendorConfig),
		mappingConfigs: make(map[string]*port.MappingConfig),
	}, nil
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

// loadDir loads a configuration directory.
func (l *Loader) loadDir(dir string) error {
	// Load routing_rules.yaml from the directory root.
	rulesPath := filepath.Join(dir, "routing_rules.yaml")
	if info, err := os.Stat(rulesPath); err == nil && !info.IsDir() {
		if err := l.loadRoutingRules(rulesPath); err != nil {
			return err
		}
	}

	// Load vendor files from vendors/ subdirectory.
	vendorsDir := filepath.Join(dir, "vendors")
	if info, err := os.Stat(vendorsDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(vendorsDir)
		if err != nil {
			return fmt.Errorf("reading vendors dir %q: %w", vendorsDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if filepath.Ext(entry.Name()) == ".yaml" {
				vendorPath := filepath.Join(vendorsDir, entry.Name())
				if err := l.loadVendorConfig(vendorPath); err != nil {
					return err
				}
			}
		}
	}

	// Load mapping files from mappings/{vendor_id}/*.yaml.
	mappingsDir := filepath.Join(dir, "mappings")
	if info, err := os.Stat(mappingsDir); err == nil && info.IsDir() {
		if err := l.loadMappingsDir(mappingsDir); err != nil {
			return err
		}
	}

	return nil
}

// loadFile loads a single YAML file, determining its type from the filename.
func (l *Loader) loadFile(path string) error {
	base := filepath.Base(path)
	if base == "routing_rules.yaml" {
		return l.loadRoutingRules(path)
	}
	// Any other .yaml file is treated as a vendor config.
	return l.loadVendorConfig(path)
}

// loadRoutingRules parses a routing_rules.yaml file.
func (l *Loader) loadRoutingRules(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var file routingRulesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	for _, item := range file.Rules {
		l.routingRules = append(l.routingRules, port.RoutingRule{
			EventType: item.EventType,
			VendorID:  item.VendorID,
		})
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

	l.vendorConfigs[file.VendorID] = vendor

	return nil
}

// loadMappingsDir loads all mapping files from a mappings/ directory.
// Structure: mappings/{vendor_id}/{event_type}.yaml
func (l *Loader) loadMappingsDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading mappings dir %q: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		vendorID := entry.Name()
		vendorDir := filepath.Join(dir, vendorID)
		vendorEntries, err := os.ReadDir(vendorDir)
		if err != nil {
			return fmt.Errorf("reading vendor mappings dir %q: %w", vendorDir, err)
		}
		for _, fe := range vendorEntries {
			if fe.IsDir() || filepath.Ext(fe.Name()) != ".yaml" {
				continue
			}
			mappingPath := filepath.Join(vendorDir, fe.Name())
			if err := l.loadMappingFile(mappingPath, vendorID); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadMappingFile parses a single mapping YAML file and stores it.
func (l *Loader) loadMappingFile(path, vendorID string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file %q: %w", path, err)
	}

	var file mappingConfigFile
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

	l.mappingConfigs[key] = &port.MappingConfig{
		EventType: eventType,
		Body: port.BodyConfig{
			Type:     file.Request.Body.Type,
			Template: file.Request.Body.Template,
		},
	}

	return nil
}

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
func (l *Loader) GetVendorConfig(vendorID string) (*port.VendorConfig, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	v, ok := l.vendorConfigs[vendorID]
	return v, ok
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
// Merges vendor request config with mapping-level body template.
func (l *Loader) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	vendor, ok := l.vendorConfigs[vendorID]
	if !ok {
		return nil, false
	}

	spec := &port.DeliverySpec{
		Mapping: port.MappingConfig{
			EventType: eventType,
			Request:   vendor.Request,
		},
	}

	// If a mapping file exists for this (vendor, event_type), use its body template.
	// Otherwise fall back to the vendor's own body config.
	mappingKey := vendorID + "/" + eventType
	if m, ok := l.mappingConfigs[mappingKey]; ok {
		spec.Mapping.Body = m.Body
	} else {
		spec.Mapping.Body = vendor.Body
	}

	return spec, true
}

// GetRoutingRules returns all routing rules matching the given event type.
func (l *Loader) GetRoutingRules(eventType string) []port.RoutingRule {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []port.RoutingRule
	for _, rule := range l.routingRules {
		if rule.EventType == eventType {
			result = append(result, rule)
		}
	}
	return result
}
