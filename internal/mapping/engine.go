package mapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xnslong/rc_xnslong/internal/port"
)

// Body type constants.
const (
	bodyTypeNone    = "none"
	bodyTypeRaw     = "raw"
	bodyTypeMapping = "mapping"
	bodyTypePlugin  = "plugin"
)

// Auth type constants.
const (
	authTypeBearer = "bearer"
	authTypeBasic  = "basic"

	authConfigKeyToken = "token"
	authConfigKeyUser  = "user"
	authConfigKeyPass  = "pass"

	authHeaderName  = "Authorization"
	authBearerPrefix = "Bearer "
	authBasicPrefix  = "Basic "
)

// Scope constants for @{payload:...} and @{item:...} references.
const (
	scopePayload = "payload"
	scopeItem    = "item"
)

// Type name constants for convertType.
const (
	typeNameString  = "string"
	typeNameInteger = "integer"
	typeNameNumber  = "number"
	typeNameBoolean = "boolean"
)

// resolveRefRe matches @{scope:path} references where scope is payload or item.
// The :path part is optional — @{item} (bare) references the current element
// value itself (for primitive arrays), while @{item:field} references a field
// on the current element object (for object arrays).
var resolveRefRe = regexp.MustCompile(`@\{(payload|item)(?::([^}]+))?\}`)

// resolveContext is the unified data context container for the mapping engine.
// It provides namespace isolation between the original notification payload
// and the current $each iteration item.
//
//	@{payload:field}  resolves against ctx.payload (original notification data).
//	@{item:field}     resolves against ctx.item field (current $each element,
//	                  for object arrays where element is a map).
//	@{item}           resolves to ctx.item itself (for primitive arrays where
//	                  element is a number, string, or bool).
type resolveContext struct {
	payload map[string]any
	item    any // current $each element; map[string]any for object arrays,
	// int/string/bool etc. for primitive arrays; nil outside $each.
}

func (c resolveContext) lookup(scope string) map[string]any {
	switch scope {
	case scopePayload:
		return c.payload
	case scopeItem:
		if m, ok := c.item.(map[string]any); ok {
			return m
		}
		return nil
	default:
		return nil
	}
}

// Engine builds HTTP requests from vendor config, mapping config, and payload.
// It resolves @{payload:field} and @{item:field} references, processes
// $source/$type/$format/$each directives, and assembles a complete http.Request.
type Engine struct{}

// NewEngine creates a new request building engine.
func NewEngine() *Engine {
	return &Engine{}
}

// BuildRequest constructs a complete http.Request from vendor config, mapping
// config, and payload. The request includes resolved URL, headers, and body.
// The URL is constructed as vendorCfg.BaseURL + mappingCfg.Request.Path,
// where Path may contain @{payload:...} references that are resolved.
// Auth is injected from vendorCfg.Auth after request construction.
func (e *Engine) BuildRequest(vendorCfg *port.VendorConfig, mappingCfg *port.MappingConfig, payload map[string]any) (*http.Request, error) {
	if vendorCfg == nil {
		return nil, nil
	}
	if mappingCfg == nil {
		return nil, nil
	}

	ctx := resolveContext{payload: payload}

	var bodyReader io.Reader
	if mappingCfg.Body.Type != "" && mappingCfg.Body.Type != "none" {
		bodyBytes, err := e.buildBody(&mappingCfg.Body, ctx)
		if err != nil {
			return nil, fmt.Errorf("build body: %w", err)
		}
		if len(bodyBytes) > 0 {
			bodyReader = bytes.NewReader(bodyBytes)
		}
	}

	// Resolve path template references (e.g. /api/v3/contacts/@{payload:user_id})
	resolvedPath, err := e.resolveField(mappingCfg.Request.Path, ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	urlStr := vendorCfg.BaseURL + tostring(resolvedPath)

	req, err := http.NewRequest(mappingCfg.Request.Method, urlStr, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	// Set headers from delivery contract
	for k, v := range mappingCfg.Request.Headers {
		req.Header.Set(k, v)
	}

	// Inject auth from vendor config (added after headers so it can override any
	// auth-related headers that were set in the contract)
	if vendorCfg.Auth != nil {
		switch vendorCfg.Auth.Type {
		case authTypeBearer:
			if token, ok := vendorCfg.Auth.Config[authConfigKeyToken].(string); ok {
				req.Header.Set(authHeaderName, authBearerPrefix+token)
			}
		case authTypeBasic:
			if user, ok := vendorCfg.Auth.Config[authConfigKeyUser].(string); ok {
				if pass, ok := vendorCfg.Auth.Config[authConfigKeyPass].(string); ok {
					auth := tostring(user) + ":" + tostring(pass)
					req.Header.Set(authHeaderName, authBasicPrefix+auth)
				}
			}
		}
	}

	return req, nil
}

// buildBody constructs the request body based on the body configuration type.
// Supported types: none, raw, mapping, plugin.
func (e *Engine) buildBody(bodyCfg *port.BodyConfig, ctx resolveContext) ([]byte, error) {
	if bodyCfg == nil {
		return nil, nil
	}
	switch bodyCfg.Type {
	case bodyTypeNone:
		return nil, nil
	case bodyTypeRaw:
		tmplBytes, err := json.Marshal(bodyCfg.Template)
		if err != nil {
			return nil, fmt.Errorf("raw body marshal: %w", err)
		}
		tmplStr := string(tmplBytes)
		return e.resolveRawBody(tmplStr, ctx)
	case bodyTypeMapping:
		return e.resolveMappingBody(bodyCfg.Template, ctx)
	case bodyTypePlugin:
		return nil, fmt.Errorf("mapper plugin not supported yet")
	default:
		return nil, fmt.Errorf("unknown body type: %s", bodyCfg.Type)
	}
}

// resolveMappingBody recursively processes a mapping template body.
func (e *Engine) resolveMappingBody(template any, ctx resolveContext) ([]byte, error) {
	resolved, err := e.resolveNode(template, ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resolved)
}

// resolveRawBody processes a raw string body by resolving @{} references
// and returning the result as bytes.
func (e *Engine) resolveRawBody(template string, ctx resolveContext) ([]byte, error) {
	resolved, err := e.resolveField(template, ctx)
	if err != nil {
		return nil, err
	}
	return []byte(tostring(resolved)), nil
}

// resolveNode recursively resolves a template node, handling strings,
// maps (including $keywords), and arrays.
func (e *Engine) resolveNode(node any, ctx resolveContext) (any, error) {
	switch v := node.(type) {
	case string:
		return e.resolveField(v, ctx)
	case map[string]any:
		// Check for $source directive (covers $source, $type, $format, $each)
		if _, ok := v[port.DirectiveSource]; ok {
			return e.resolveSourceDirective(v, ctx)
		}
		// Regular map: resolve each value, handle $$ prefix escaping
		result := make(map[string]any, len(v))
		for key, val := range v {
			// $$ prefix escapes to literal $
			actualKey := strings.TrimPrefix(key, "$$")
			resolved, err := e.resolveNode(val, ctx)
			if err != nil {
				return nil, err
			}
			result[actualKey] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, val := range v {
			resolved, err := e.resolveNode(val, ctx)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	default:
		// Numbers, bools, nil — return as-is
		return v, nil
	}
}

// resolveSourceDirective handles $source/$type/$format/$each keyword directives.
// If the directive contains $each, it delegates to resolveEachDirective for
// array traversal mapping.
// $type conversion happens before $format conversion.
func (e *Engine) resolveSourceDirective(v map[string]any, ctx resolveContext) (any, error) {
	// Scenario A: $source + $each → array traversal mapping
	if _, ok := v[port.DirectiveEach]; ok {
		return e.resolveEachDirective(v, ctx)
	}

	// Scenario B: $source (optional $type/$format) → single value extraction + conversion
	sourceExpr, ok := v[port.DirectiveSource].(string)
	if !ok {
		return nil, fmt.Errorf("$source must be a string")
	}

	// Resolve @{scope:path} reference to get the raw value
	raw, err := e.resolveField(sourceExpr, ctx)
	if err != nil {
		return nil, err
	}

	// Type conversion before format conversion
	if typeNameVal, ok := v[port.DirectiveType]; ok {
		typeName, ok := typeNameVal.(string)
		if !ok {
			return nil, fmt.Errorf("$type must be a string")
		}
		converted, err := convertType(raw, typeName)
		if err != nil {
			return nil, err
		}
		raw = converted
	}

	// Format conversion (only after type conversion)
	if formatVal, ok := v[port.DirectiveFormat]; ok {
		format, ok := formatVal.(string)
		if !ok {
			return nil, fmt.Errorf("$format must be a string")
		}
		return formatValue(tostring(raw), format)
	}

	return raw, nil
}

// resolveEachDirective handles $source + $each array traversal mapping.
// It resolves $source to get the source array, then for each element
// creates an extended context with the element as ctx.item and applies
// the $each template mapping.
func (e *Engine) resolveEachDirective(v map[string]any, ctx resolveContext) (any, error) {
	sourceExpr, ok := v[port.DirectiveSource].(string)
	if !ok {
		return nil, fmt.Errorf("$source must be a string for $each")
	}

	srcRaw, err := e.resolveField(sourceExpr, ctx)
	if err != nil {
		return nil, err
	}

	srcArray, ok := srcRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("$source %q must resolve to an array, got %T", sourceExpr, srcRaw)
	}

	eachTemplate, ok := v[port.DirectiveEach]
	if !ok {
		return nil, fmt.Errorf("$each directive missing")
	}

	result := make([]any, 0, len(srcArray))
	for _, elem := range srcArray {
		// Support both object arrays (map[string]any) and primitive arrays
		// (string, int, float64, bool, nil).
		var itemCtx resolveContext
		if elemMap, ok := elem.(map[string]any); ok {
			itemCtx = resolveContext{payload: ctx.payload, item: elemMap}
		} else {
			itemCtx = resolveContext{payload: ctx.payload, item: elem}
		}
		mapped, err := e.resolveNode(eachTemplate, itemCtx)
		if err != nil {
			return nil, fmt.Errorf("$each mapping failed: %w", err)
		}
		result = append(result, mapped)
	}

	return result, nil
}

// resolveField resolves a field expression which may contain @{scope:path}
// references. It is the single entry point for all @{} reference replacement.
//
// Behavior depends on the expression form:
//   - No @{} references: returns expr as-is (static string)
//   - Pure "@{scope}" (bare scope, no :path): returns the scope's value directly.
//     "@{item}" returns the current $each element value itself (supports
//     primitive values).
//   - Pure "@{scope:path}" (single reference, no prefix/suffix): returns the
//     original typed value from ctx (preserves int/bool/nil types).
//   - Mixed content (prefix/suffix/multiple @{}): concatenates everything as string.
//
// scope can be "payload" or "item", resolved against ctx.payload and ctx.item
// respectively.
func (e *Engine) resolveField(expr string, ctx resolveContext) (any, error) {
	loc := resolveRefRe.FindStringIndex(expr)
	if loc == nil {
		// No template references, return as-is
		return expr, nil
	}

	// Pure reference (no prefix/suffix, single reference) — return original type
	if loc[0] == 0 && loc[1] == len(expr) {
		matches := resolveRefRe.FindStringSubmatch(expr)
		scope := matches[1]
		path := matches[2]
		if path == "" {
			return e.getScopeValue(scope, ctx), nil
		}
		val := getNestedField(ctx.lookup(scope), path)
		return val, nil
	}

	// Mixed content with prefix/suffix or multiple references — return string
	result := resolveRefRe.ReplaceAllStringFunc(expr, func(match string) string {
		matches := resolveRefRe.FindStringSubmatch(match)
		scope := matches[1]
		path := matches[2]
		if path == "" {
			return tostring(e.getScopeValue(scope, ctx))
		}
		return tostring(getNestedField(ctx.lookup(scope), path))
	})
	return result, nil
}

// getScopeValue returns the raw value for a bare @{scope} reference.
// For "item" returns ctx.item directly (supports primitive values).
// For "payload" returns ctx.payload as-is.
func (e *Engine) getScopeValue(scope string, ctx resolveContext) any {
	switch scope {
	case scopePayload:
		return ctx.payload
	case scopeItem:
		return ctx.item
	default:
		return nil
	}
}

// getNestedField traverses a map by dot-separated path and returns the value.
// Returns nil if any segment in the path is missing or non-map.
func getNestedField(data map[string]any, path string) any {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	current := data
	for i, part := range parts {
		val, ok := current[part]
		if !ok {
			return nil
		}
		if i == len(parts)-1 {
			return val
		}
		next, ok := val.(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return nil
}

// convertType coerces val to the specified type.
// Supported target types: string, integer, number, boolean.
func convertType(val any, typeName string) (any, error) {
	switch typeName {
	case typeNameString:
		return tostring(val), nil
	case typeNameInteger:
		switch v := val.(type) {
		case int:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			return int64(v), nil
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to integer", v)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to integer", val)
		}
	case typeNameNumber:
		switch v := val.(type) {
		case int:
			return v, nil
		case int64:
			return v, nil
		case float64:
			return v, nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to number", v)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to number", val)
		}
	case typeNameBoolean:
		switch v := val.(type) {
		case bool:
			return v, nil
		case int:
			return v != 0, nil
		case int64:
			return v != 0, nil
		case float64:
			return v != 0, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("cannot convert %q to boolean", v)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to boolean", val)
		}
	default:
		return nil, fmt.Errorf("unknown type: %s", typeName)
	}
}

// formatValue applies a format transformation to a string value.
// Used for time format conversions (e.g. unix timestamp to "yyyy-MM-dd").
func formatValue(val, format string) (string, error) {
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return "", fmt.Errorf("cannot parse %q as timestamp: %w", val, err)
	}
	return time.Unix(n, 0).UTC().Format(format), nil
}

// tostring converts any value to its string representation.
func tostring(val any) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case int:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
