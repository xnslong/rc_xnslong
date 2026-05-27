package mapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xnslong/rc_xnslong/internal/port"
)

var payloadFieldRe = regexp.MustCompile(`@\{payload\.([^}]+)\}`)

// Engine builds HTTP requests from vendor config, mapping config, and payload.
// It resolves @{payload.field} references, processes $source/$type/$format
// directives, and assembles a complete http.Request.
type Engine struct{}

// NewEngine creates a new request building engine.
func NewEngine() *Engine {
	return &Engine{}
}

// BuildRequest constructs a complete http.Request from vendor config, mapping
// config, and payload. The request includes resolved URL, headers, and body.
func (e *Engine) BuildRequest(vendorCfg *port.VendorConfig, mappingCfg *port.MappingConfig, payload map[string]any) (*http.Request, error) {
	if vendorCfg == nil {
		return nil, nil
	}

	var bodyReader io.Reader
	if mappingCfg != nil && mappingCfg.Body.Type != "" && mappingCfg.Body.Type != "none" {
		bodyBytes, err := e.buildBody(&mappingCfg.Body, payload)
		if err != nil {
			return nil, fmt.Errorf("build body: %w", err)
		}
		if len(bodyBytes) > 0 {
			bodyReader = bytes.NewReader(bodyBytes)
		}
	}

	req, err := http.NewRequest(vendorCfg.Request.Method, vendorCfg.Request.URLTmpl, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	for k, v := range vendorCfg.Request.Headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

// resolveString resolves @{payload.field} references in a template string.
// Pure @{payload.field} (no prefix/suffix) returns the string representation of the value;
// mixed content is concatenated as string.
func (e *Engine) resolveString(tmpl string, payload map[string]any) (string, error) {
	loc := payloadFieldRe.FindStringIndex(tmpl)
	if loc == nil {
		// No template references, return as-is
		return tmpl, nil
	}

	// Pure reference (no prefix/suffix, single reference)
	if loc[0] == 0 && loc[1] == len(tmpl) {
		path := tmpl[len("@{payload.") : len(tmpl)-1]
		val := getNestedField(payload, path)
		if val == nil {
			log.Printf("warning: payload field %q not found", path)
		}
		return tostring(val), nil
	}

	// Mixed content with prefix/suffix or multiple references
	result := payloadFieldRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		path := match[len("@{payload.") : len(match)-1]
		val := getNestedField(payload, path)
		if val == nil {
			log.Printf("warning: payload field %q not found", path)
		}
		return tostring(val)
	})
	return result, nil
}

// buildBody constructs the request body based on the body configuration type.
// Supported types: none, raw, mapping, plugin.
func (e *Engine) buildBody(bodyCfg *port.BodyConfig, payload map[string]any) ([]byte, error) {
	if bodyCfg == nil {
		return nil, nil
	}
	switch bodyCfg.Type {
	case "none":
		return nil, nil
	case "raw":
		tmplBytes, err := json.Marshal(bodyCfg.Template)
		if err != nil {
			return nil, fmt.Errorf("raw body marshal: %w", err)
		}
		tmplStr := string(tmplBytes)
		return e.resolveRawBody(tmplStr, payload)
	case "mapping":
		return e.resolveMappingBody(bodyCfg.Template, payload)
	case "plugin":
		return nil, fmt.Errorf("mapper plugin not supported yet")
	default:
		return nil, fmt.Errorf("unknown body type: %s", bodyCfg.Type)
	}
}

// resolveMappingBody recursively processes a mapping template body.
func (e *Engine) resolveMappingBody(template any, payload map[string]any) ([]byte, error) {
	resolved, err := e.resolveNode(template, payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resolved)
}

// resolveRawBody processes a raw string body by resolving @{} references.
func (e *Engine) resolveRawBody(template string, payload map[string]any) ([]byte, error) {
	resolved, err := e.resolveString(template, payload)
	if err != nil {
		return nil, err
	}
	return []byte(resolved), nil
}

// resolveNode recursively resolves a template node, handling strings,
// maps (including $keywords), and arrays.
func (e *Engine) resolveNode(node any, payload map[string]any) (any, error) {
	switch v := node.(type) {
	case string:
		// Resolve @{...} references
		return e.resolveString(v, payload)
	case map[string]any:
		// Check for $source directive
		if _, ok := v["$source"]; ok {
			return e.resolveSourceDirective(v, payload)
		}
		// Regular map: resolve each value, handle $$ prefix escaping
		result := make(map[string]any, len(v))
		for key, val := range v {
			// $$ prefix escapes to literal $
			actualKey := strings.TrimPrefix(key, "$$")
			resolved, err := e.resolveNode(val, payload)
			if err != nil {
				return nil, err
			}
			result[actualKey] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, val := range v {
			resolved, err := e.resolveNode(val, payload)
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

// resolveSourceDirective handles $source/$type/$format keyword directives.
// $type conversion happens before $format conversion.
func (e *Engine) resolveSourceDirective(v map[string]any, payload map[string]any) (any, error) {
	sourceExpr, ok := v["$source"].(string)
	if !ok {
		return nil, fmt.Errorf("$source must be a string")
	}

	// Resolve @{payload.field} reference to get the raw value
	raw, err := e.resolveField(sourceExpr, payload)
	if err != nil {
		return nil, err
	}

	// Type conversion before format conversion
	if typeNameVal, ok := v["$type"]; ok {
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
	if formatVal, ok := v["$format"]; ok {
		format, ok := formatVal.(string)
		if !ok {
			return nil, fmt.Errorf("$format must be a string")
		}
		return formatValue(tostring(raw), format)
	}

	return raw, nil
}

// resolveField resolves a field expression which may be a pure @{payload.field}
// reference or a mixed string with prefixes/suffixes.
func (e *Engine) resolveField(expr string, payload map[string]any) (any, error) {
	loc := payloadFieldRe.FindStringIndex(expr)
	if loc == nil {
		// No template references, return as-is
		return expr, nil
	}

	// Pure reference (no prefix/suffix, single reference) — return original type
	if loc[0] == 0 && loc[1] == len(expr) {
		path := expr[len("@{payload.") : len(expr)-1]
		val := getNestedField(payload, path)
		return val, nil
	}

	// Mixed content with prefix/suffix or multiple references — return string
	result := payloadFieldRe.ReplaceAllStringFunc(expr, func(match string) string {
		path := match[len("@{payload.") : len(match)-1]
		return tostring(getNestedField(payload, path))
	})
	return result, nil
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
	case "string":
		return tostring(val), nil
	case "integer":
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
	case "number":
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
	case "boolean":
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
