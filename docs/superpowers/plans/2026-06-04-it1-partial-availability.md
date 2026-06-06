# IT1: ConfigLoader Partial Availability + Interface Signature Change

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox syntax.

**Goal:** Convert ConfigProvider from `(*T, bool)` to `(*T, error)` with `LoadedValue[T]` error/value binding, implement partial availability (single file errors don't abort the whole load), restructure route files from flat `route.yaml` to per-event `routes/{event}.yaml`, and add cross-config validation.

**Architecture:** 
- `ConfigProvider` interface changes signature on all 4 methods, propagating to Loader + consumers + mock
- `LoadedValue[T]` wraps value+error into a single struct inside each map, eliminating parallel error maps
- Route files become per-event (`routes/{event}.yaml`), each mapping to a `map[eventType]*LoadedValue[[]*RoutingRule]` entry
- Loading methods use `recordError()` helper instead of `return err` — only global directory errors are fatal
- Schema storage drops `json.Marshal` round-trip, stores `map[string]any` directly

**Tech Stack:** Go 1.19+ (generics with `LoadedValue[T]`), gopkg.in/yaml.v3, rs/zerolog

---

## File Inventory

| File | Action | What |
|------|--------|------|
| `internal/port/config.go` | Modify | Interface sigs, `ErrNotConfigured` sentinel |
| `internal/config/loader.go` | Large modify | `LoadedValue` type, `recordError`, route format, struct fields, Get* methods, cross-config validation, structured logging |
| `internal/delivery/worker.go` | Modify | `ok` → `err` check (behavior unchanged, IT2 changes behavior) |
| `internal/ingestion/service.go` | Modify | `!ok` → `err != nil` (behavior unchanged) |
| `internal/routing/dispatcher.go` | Modify | adapt `GetRoutingRules` to `(*T, error)` (behavior unchanged) |
| `test/e2e/suite.go` | Modify | `ok` → `err` check on `GetVendorConfig` |
| `internal/delivery/worker_test.go` | Modify | `MockConfigProvider` method sigs, `.On()` returns |
| `internal/config/loader_test.go` | Modify | Update test calls from `ok` to `err` |
| `test/e2e/testdata/common/events/order/route.yaml` | Replace | Per-event split |
| `test/e2e/testdata/tc37/events/tc37/route.yaml` | Replace | Per-event split (19 files) |
| `internal/config/testdata/events/order/route.yaml` | Replace | Per-event split |

---

### Task 1: Add type definitions (ErrNotConfigured, LoadedValue, routesFile)

**Files:**
- Modify: `internal/port/config.go`
- Modify: `internal/config/loader.go`

- [ ] **Step 1: Add `ErrNotConfigured` to port/config.go**

Add after the imports:

```go
var ErrNotConfigured = errors.New("config not configured")
```

- [ ] **Step 2: Add `LoadedValue[T]` and `routesFile` to config/loader.go**

Insert after the existing `eventSchemaFile` type:

```go
// LoadedValue wraps a config value with its load error.
// Value is nil when Error != nil; Error is nil when load succeeded.
// This lets consumers answer "is this item available?" from one lookup
// instead of checking separate error maps.
type LoadedValue[T any] struct {
    Value T
    Error error
}

// routesFile is the YAML representation of events/{biz}/routes/{event}.yaml.
type routesFile struct {
    EventType string `yaml:"event_type"`
    Routes    []struct {
        VendorID string `yaml:"vendor_id"`
    } `yaml:"routes"`
}
```

- [ ] **Step 3: Remove old bizRouteFile type**

Delete the `bizRouteFile` struct (lines 27-30). It's replaced by `routesFile`.

- [ ] **Step 4: Update port.ConfigProvider interface**

In `internal/port/config.go`, replace all 4 method signatures:

```go
type ConfigProvider interface {
    GetVendorConfig(vendorID string) (*VendorConfig, error)
    GetDeliverySpec(vendorID, eventType string) (*DeliverySpec, error)
    GetRoutingRules(eventType string) ([]RoutingRule, error)
    GetEventSchema(eventType string) (map[string]any, error)
}
```

- [ ] **Step 5: Run `go vet` to verify basic compilation so far**

Run: `go vet ./internal/port/...`

Expected: OK (no implementors yet, interface-only change compiles)

---

### Task 2: Refactor Loader struct and Get* methods

**Files:**
- Modify: `internal/config/loader.go`

- [ ] **Step 1: Update Loader struct fields**

Replace the existing Loader struct:

```go
type Loader struct {
    mu    sync.RWMutex
    paths []string

    routingRules      map[string]*LoadedValue[[]*port.RoutingRule] // key: eventType
    vendorConfigs     map[string]*LoadedValue[*port.VendorConfig]
    deliveryContracts map[string]*LoadedValue[*deliveryContractFile] // key: "vendorID/eventType"
    eventSchemas      map[string]*LoadedValue[map[string]any]       // key: eventType
}
```

- [ ] **Step 2: Update NewLoader to initialize new map types**

```go
func NewLoader(paths ...string) (*Loader, error) {
    return &Loader{
        paths:             paths,
        routingRules:      make(map[string]*LoadedValue[[]*port.RoutingRule]),
        vendorConfigs:     make(map[string]*LoadedValue[*port.VendorConfig]),
        deliveryContracts: make(map[string]*LoadedValue[*deliveryContractFile]),
        eventSchemas:      make(map[string]*LoadedValue[map[string]any]),
    }, nil
}
```

- [ ] **Step 3: Add `recordError` helper**

Add after `NewLoader`:

```go
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
    // TODO: increment config.load.failure counter (prometheus/metrics)
        Msg("config load failure")
}
```

- [ ] **Step 4: Rewrite `GetVendorConfig`**

Replace existing method (lines 490-496):

```go
func (l *Loader) GetVendorConfig(vendorID string) (*port.VendorConfig, error) {
    l.mu.RLock()
    defer l.mu.RUnlock()

    lv, ok := l.vendorConfigs[vendorID]
    if !ok {
        return nil, port.ErrNotConfigured
    }
    return lv.Value, lv.Error
}
```

- [ ] **Step 5: Rewrite `GetDeliverySpec`**

Replace existing method (lines 512-556):

```go
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
```

- [ ] **Step 6: Rewrite `GetRoutingRules`**

Replace existing method (lines 559-570):

```go
func (l *Loader) GetRoutingRules(eventType string) ([]port.RoutingRule, error) {
    l.mu.RLock()
    defer l.mu.RUnlock()

    lv, ok := l.routingRules[eventType]
    if !ok {
        return nil, port.ErrNotConfigured
    }
    return lv.Value, lv.Error
}
```

- [ ] **Step 7: Rewrite `GetEventSchema`**

Replace existing method (lines 573-579):

```go
func (l *Loader) GetEventSchema(eventType string) (map[string]any, error) {
    l.mu.RLock()
    defer l.mu.RUnlock()

    lv, ok := l.eventSchemas[eventType]
    if !ok {
        return nil, port.ErrNotConfigured
    }
    return lv.Value, lv.Error
}
```

- [ ] **Step 8: Run `go vet ./internal/config/...`**

Expected: compiles (external callers not yet updated, but that's next task)

---

### Task 3: Rewrite internal loading methods with partial availability

**Files:**
- Modify: `internal/config/loader.go`

- [ ] **Step 1: Add `existsAndIsDir` helper**

Insert after `convertResponseJudgment`:

```go
// existsAndIsDir returns true if path exists and is a directory.
func existsAndIsDir(path string) bool {
    info, err := os.Stat(path)
    return err == nil && info.IsDir()
}
```

- [ ] **Step 2: Rewrite `Load` method**

Replace existing (lines 118-136):

```go
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
```

- [ ] **Step 3: Rewrite `loadDir` — only fatal if BOTH events/ and vendors/ missing**

Replace existing (lines 151-170):

```go
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
```

- [ ] **Step 4: Rewrite `loadFile` — use recordError**

Replace existing (lines 173-183):

```go
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
```

Note: `route.yaml` case is removed since route files are no longer single-file loaded. Single files can load vendor configs.

- [ ] **Step 5: Rewrite `loadRoutesFromEventsDir` — scan routes/*.yaml, new format**

Replace existing (lines 188-232):

```go
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

// loadBizRoute parses a single events/{biz}/routes/{event}.yaml file.
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
```

Delete the old `loadBizRoute` method (the one that returned error with `bizRouteFile`).

- [ ] **Step 6: Rewrite `loadVendorsDir` — use recordError**

Replace existing (lines 237-263):

```go
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
        if existsAndIsDir(vendorPath) || !fileExists(vendorPath) {
            // vendor.yaml missing — record error
            l.recordError("vendor", vendorID, vendorPath,
                fmt.Errorf("vendor.yaml not found"))
        } else {
            l.loadVendorConfig(vendorPath)
        }

        l.loadDeliveryContractsForVendor(vendorDir, vendorID)
    }
}
```

- [ ] **Step 7: Add `fileExists` helper**

```go
func fileExists(path string) bool {
    info, err := os.Stat(path)
    return err == nil && !info.IsDir()
}
```

- [ ] **Step 8: Rewrite `loadVendorConfig` — use recordError, return nothing**

Replace existing (lines 267-314):

```go
func (l *Loader) loadVendorConfig(path string) {
    data, err := os.ReadFile(path)
    if err != nil {
        l.recordError("vendor", filepath.Dir(path), path,
            fmt.Errorf("reading file: %w", err))
        return
    }

    var file vendorConfigFile
    if err := yaml.Unmarshal(data, &file); err != nil {
        l.recordError("vendor", filepath.Dir(path), path,
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
```

- [ ] **Step 9: Rewrite `loadDeliveryContractsForVendor` — use recordError**

Replace existing (lines 319-346):

```go
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
```

- [ ] **Step 10: Rewrite `loadDeliveryContract` — use recordError + LoadedValue**

Replace existing (lines 350-374):

```go
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
```

- [ ] **Step 11: Rewrite `loadHierarchicalEventSchemas` — use recordError**

Replace existing (lines 379-411):

```go
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
```

- [ ] **Step 12: Rewrite `loadEventSchemaFile` — store map[string]any directly**

Replace existing (lines 415-441):

```go
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
```

- [ ] **Step 13: Add cross-config validation method**

Add after `loadHierarchicalEventSchemas`:

```go
// validateCrossConfig checks for consistency between independently-loaded
// config items. Failures are logged but do not affect startup.
func (l *Loader) validateCrossConfig() {
    l.mu.RLock()
    defer l.mu.RUnlock()

    // 1. Routing rules reference existing vendors
    for eventType, lv := range l.routingRules {
        if lv.Error != nil || lv.Value == nil {
            continue
        }
        for _, rule := range lv.Value {
            if _, ok := l.vendorConfigs[rule.VendorID]; !ok {
                log.Warn().
                    Str("module", "config.loader").
                    Str("event", "validation_error").
                    Str("type", "routing_vendor_not_found").
                    Str("vendor", rule.VendorID).
                    Str("event_type", eventType).
                    Msg("routing rule references non-existent vendor")
            }
        }
    }

    // 2. Template fields reference existence in schema
    // (future enhancement)
}
```

- [ ] **Step 14: Add structured logging import**

Ensure `import` block at top of `loader.go` includes `"github.com/rs/zerolog/log"`.

- [ ] **Step 15: Run `go vet ./internal/config/...`**

Expected: Compiles clean (external callers may fail, that's next task).

---

### Task 4: Update downstream callers — signature only, no behavior change

**Files:**
- Modify: `internal/delivery/worker.go`
- Modify: `internal/ingestion/service.go`
- Modify: `internal/routing/dispatcher.go`
- Modify: `test/e2e/suite.go`

- [ ] **Step 1: Update Worker's GetVendorConfig call**

In `internal/delivery/worker.go`, find the `ProcessMessage` method. Change:

```go
// Before:
vendorCfg, ok := p.deps.Config.GetVendorConfig(task.VendorID)
if !ok {
    return fmt.Errorf("vendor config not found: %s", task.VendorID)
}

// After:
vendorCfg, err := p.deps.Config.GetVendorConfig(task.VendorID)
if err != nil {
    return fmt.Errorf("vendor config not found: %w", err)
}
```

Note: `!ok` → `err != nil`, wrapping added. Same behavior: returns error → nack.

- [ ] **Step 2: Update Worker's GetDeliverySpec call**

```go
// Before:
spec, ok := p.deps.Config.GetDeliverySpec(task.VendorID, task.EventType)
if !ok {
    return fmt.Errorf("delivery spec not found: %s/%s", task.VendorID, task.EventType)
}

// After:
spec, err := p.deps.Config.GetDeliverySpec(task.VendorID, task.EventType)
if err != nil {
    return fmt.Errorf("delivery spec not found: %w", err)
}
```

- [ ] **Step 3: Update Ingestion's GetEventSchema call**

In `internal/ingestion/service.go`, change `validateSchema`:

```go
// Before:
schemaDef, ok := s.cfg.GetEventSchema(eventType)
if !ok {
    return &ErrEventNotFound{EventType: eventType}
}

// After:
schemaDef, err := s.cfg.GetEventSchema(eventType)
if err != nil {
    return &ErrEventNotFound{EventType: eventType}
}
```

- [ ] **Step 4: Add validator adapter for `map[string]any`**

In `internal/ingestion/validator.go`, add a new `validate` overload that takes `map[string]any`.
IT2 will add caching; this is the minimal adapter to keep things compiling:

```go
func (v *schemaValidator) validate(schemaMap map[string]any, payload map[string]any) []ValidationError {
    // Temp bridge: map -> JSON -> schemaNode.
    // IT2 adds proper parse-once cache that skips this round-trip.
    schemaJSON, err := json.Marshal(schemaMap)
    if err != nil {
        return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
    }
    var schema schemaNode
    if err := json.Unmarshal(schemaJSON, &schema); err != nil {
        return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
    }
    return validateNode(schema, payload, "payload")
}
```

Make sure `encoding/json` is imported in `validator.go`.

- [ ] **Step 4b: Update Ingestion's validateSchema call**

In `internal/ingestion/service.go`, change the validator call:

```go
// Before:
valErrs := s.validator.validate(schemaDef, params.Payload)

// After:
valErrs := s.validator.validate(schemaDef, params.Payload)
```

The method name stays the same — Go handles overloading via different parameter types.

- [ ] **Step 5: Update Router's GetRoutingRules call**

In `internal/routing/dispatcher.go`:

```go
// Before:
rules := d.cfg.GetRoutingRules(notif.EventType)

// After:
rules, err := d.cfg.GetRoutingRules(notif.EventType)
if err != nil {
    // Error means this event type's route file failed to load
    // Treat same as no rules — notify will FAILED
}
```

Since the existing code already handles `len(rules) == 0` gracefully, we need to also handle `err != nil`:

```go
rules, err := d.cfg.GetRoutingRules(notif.EventType)
if err != nil {
    log.Warn().Err(err).Str("notification_id", notificationID).
        Str("event_type", notif.EventType).
        Msg("routing rules unavailable, marking notification as FAILED")
    if err := d.db.UpdateNotificationStatus(ctx, notificationID, "FAILED"); err != nil {
        return err
    }
    return nil
}
```

Wait, but there's already code below that does `len(rules) == 0 → FAILED`. I can combine both checks:

```go
rules, err := d.cfg.GetRoutingRules(notif.EventType)
if err != nil || len(rules) == 0 {
    if err != nil {
        log.Warn().Err(err).Str(...).Msg("routing rules unavailable, marking as FAILED")
    } else {
        log.Warn().Str(...).Msg("no routing rules matched, marking as FAILED")
    }
    d.db.UpdateNotificationStatus(ctx, notificationID, "FAILED")
    return nil
}
```

Actually, let me keep it simple for IT1 — just change the signature and handle both error conditions the same way (FAILED). The detailed error distinction is IT2's behavioral change.

```go
rules, err := d.cfg.GetRoutingRules(notif.EventType)
if err != nil || len(rules) == 0 {
    log.Warn().Str("notification_id", notificationID).
        Str("event_type", notif.EventType).Msg("no routing rules matched, marking notification as FAILED")
    if err := d.db.UpdateNotificationStatus(ctx, notificationID, "FAILED"); err != nil {
        return err
    }
    return nil
}
```

Wait actually I need to check whether the existing code also had the `err` + `len(rules)` check separate... Let me recall the original:

From the earlier analysis:
```go
rules := d.cfg.GetRoutingRules(notif.EventType)
if len(rules) == 0 {
    log.Warn().Str(...).Msg("no routing rules matched, marking notification as FAILED")
    if err := d.db.UpdateNotificationStatus(ctx, notificationID, "FAILED"); err != nil {
        return err
    }
    return nil
}
```

So the compile-safe change is:
```go
rules, err := d.cfg.GetRoutingRules(notif.EventType)
if err != nil || len(rules) == 0 {
```

And keep the body the same.

- [ ] **Step 6: Update suite.go GetVendorConfig call**

In `test/e2e/suite.go` around line 142:

```go
// Before:
vendorCfg, ok := loader.GetVendorConfig(vendorID)
if !ok {
    return fmt.Errorf("vendor %s not found in config", vendorID)
}

// After:
vendorCfg, err := loader.GetVendorConfig(vendorID)
if err != nil {
    return fmt.Errorf("vendor %s not found in config: %w", vendorID, err)
}
```

- [ ] **Step 7: Run `go build ./...`**

Expected: Compiles everywhere. If not, fix any missed callers.

---

### Task 5: Update MockConfigProvider in worker_test.go

**Files:**
- Modify: `internal/delivery/worker_test.go`

- [ ] **Step 1: Update MockConfigProvider methods**

Replace the 4 mock methods (lines 123-145):

```go
func (m *MockConfigProvider) GetVendorConfig(vendorID string) (*port.VendorConfig, error) {
    args := m.Called(vendorID)
    cfg, _ := args.Get(0).(*port.VendorConfig)
    return cfg, args.Error(1)
}

func (m *MockConfigProvider) GetDeliverySpec(vendorID, eventType string) (*port.DeliverySpec, error) {
    args := m.Called(vendorID, eventType)
    spec, _ := args.Get(0).(*port.DeliverySpec)
    return spec, args.Error(1)
}

func (m *MockConfigProvider) GetRoutingRules(eventType string) ([]port.RoutingRule, error) {
    args := m.Called(eventType)
    rules, _ := args.Get(0).([]port.RoutingRule)
    return rules, args.Error(1)
}

func (m *MockConfigProvider) GetEventSchema(eventType string) (map[string]any, error) {
    args := m.Called(eventType)
    data, _ := args.Get(0).(map[string]any)
    return data, args.Error(1)
}
```

- [ ] **Step 2: Update all `.On()` calls' return tuples**

The existing pattern is `.Return(value, true)` — change to `.Return(value, nil)`:

```bash
grep -n 'mockConfig.On(' internal/delivery/worker_test.go | grep 'Return'
```

For each one: `.Return(vendorCfg, true)` → `.Return(vendorCfg, nil)`, `.Return(deliverySpec, true)` → `.Return(deliverySpec, nil)`.

The `mock.Anything` arguments for `GetRoutingRules` and `GetEventSchema`:
- `mockConfig.On("GetRoutingRules", mock.Anything).Return(mockRoutingRules)` → `mockConfig.On("GetRoutingRules", mock.Anything).Return(mockRoutingRules, nil)`
- `mockConfig.On("GetEventSchema", mock.Anything).Return(mockSchema, true)` → `mockConfig.On("GetEventSchema", mock.Anything).Return(mockSchema, nil)`

Let me count the exact changes needed. From the grep earlier:
- 6x `mockConfig.On("GetVendorConfig", ...).Return(testVendorCfg, true)` → 6x `.Return(testVendorCfg, nil)`
- 6x `mockConfig.On("GetDeliverySpec", ...).Return(testDeliverySpec, true)` → 6x `.Return(testDeliverySpec, nil)`
- Any `mockConfig.On("GetRoutingRules"...)...` 
- Any `mockConfig.On("GetEventSchema"...)...`

Let me just write the sed command.

- [ ] **Step 3: Run worker tests**

Run: `go test ./internal/delivery/... -v -run TestWorker`

Expected: Tests pass (same behavior, just different return types).

---

### Task 6: Restructure testdata route files

**Files:**
- Replace: `internal/config/testdata/events/order/route.yaml` → `internal/config/testdata/events/order/routes/order.paid.yaml`
- Replace: all other `route.yaml` files with per-event route files

- [ ] **Step 1: Create new route file structure for config testdata**

Delete `internal/config/testdata/events/order/route.yaml`.

Create `internal/config/testdata/events/order/routes/order.paid.yaml`:

```yaml
event_type: "order.paid"
routes:
  - vendor_id: "crm_system"
  - vendor_id: "ad_platform"
```

- [ ] **Step 2: Create new route files for E2E common testdata**

Delete `test/e2e/testdata/common/events/order/route.yaml`.

Create `test/e2e/testdata/common/events/order/routes/order.paid.yaml`:

```yaml
event_type: "order.paid"
routes:
  - vendor_id: "crm_system"
  - vendor_id: "ad_platform"
```

- [ ] **Step 3: Create new route files for E2E tc37 testdata**

Delete `test/e2e/testdata/tc37/events/tc37/route.yaml`.

For each of the 19 event types in the old file, create `test/e2e/testdata/tc37/events/tc37/routes/{event}.yaml`:

Example: `test/e2e/testdata/tc37/events/tc37/routes/tc371.field_ref.yaml`:
```yaml
event_type: "tc371.field_ref"
routes:
  - vendor_id: "mapping_vendor"
```

Create all 19 files, one per event type from the old route.yaml.

- [ ] **Step 4: Run config tests**

Run: `go test ./internal/config/... -v`

Expected: All existing tests pass after adapting the test code (Task 7).

---

### Task 7: Update loader tests

**Files:**
- Modify: `internal/config/loader_test.go`

- [ ] **Step 1: Update all existing tests**

Change every `loader.GetRoutingRules(...)` call from `(rules, require.NoError)` pattern to handle `(rules, err)`:

For `TestConfigLoader_GetRoutingRules`:
```go
rules, err := loader.GetRoutingRules("order.paid")
require.NoError(t, err)
require.Len(t, rules, 2, ...)
```

For `TestConfigLoader_GetVendorConfig`:
```go
vendor, err := loader.GetVendorConfig("crm_system")
require.NoError(t, err)
require.NotNil(t, vendor)
```

For `TestConfigLoader_GetDeliverySpec`:
```go
spec, err := loader.GetDeliverySpec("crm_system", "order.paid")
require.NoError(t, err)
require.NotNil(t, spec)
```

For `TestConfigLoader_GetDeliverySpec_NoContract`:
```go
spec, err := loader.GetDeliverySpec("ad_platform", "user.registered")
require.NoError(t, err)
require.NotNil(t, spec)
```

For `TestConfigLoader_GetEventSchema`:
```go
schema, err := loader.GetEventSchema("order.paid")
require.NoError(t, err)
require.NotNil(t, schema)
```

For `TestConfigLoader_Load_Error`:
This one tests a nonexistent path. `Load()` should still return an error for missing paths. Keep as-is.

- [ ] **Step 2: Add nil-LoadedValue assertion for GetEventSchema**

Since schema is now `map[string]any`, update the assertion:

```go
schema, err := loader.GetEventSchema("order.paid")
require.NoError(t, err)
require.NotNil(t, schema)
assert.Contains(t, schema, "type")
```

- [ ] **Step 3: Run config tests**

Run: `go test ./internal/config/... -v`

Expected: All 7 tests pass.

---

### Task 8: Run full compilation and verification

- [ ] **Step 1: Build**

```bash
go build ./...
```

Expected: Builds without errors.

- [ ] **Step 2: Vet**

```bash
go vet ./...
```

Expected: No vet warnings.

- [ ] **Step 3: Run all unit tests**

```bash
go test ./internal/... -v 2>&1 | head -50
```

Expected: All tests pass. If any fail, fix them.

- [ ] **Step 4: Cross-platform build check**

```bash
GOOS=windows go build ./...
GOOS=darwin go build ./...
GOOS=linux go build ./...
```

Expected: All three build without errors.
