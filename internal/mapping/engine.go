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

const (
	bodyTypeNone    = "none"
	bodyTypeRaw     = "raw"
	bodyTypeMapping = "mapping"
	bodyTypePlugin  = "plugin"
)

const (
	authTypeBearer = "bearer"
	authTypeBasic  = "basic"

	authConfigKeyToken = "token"
	authConfigKeyUser  = "user"
	authConfigKeyPass  = "pass"

	authHeaderName   = "Authorization"
	authBearerPrefix = "Bearer "
	authBasicPrefix  = "Basic "
)

const (
	scopePayload = "payload"
	scopeItem    = "item"
)

const (
	typeNameString  = "string"
	typeNameInteger = "integer"
	typeNameNumber  = "number"
	typeNameBoolean = "boolean"
)

var resolveRefRe = regexp.MustCompile(`@\{(payload|item)(?::([^}]+))?\}`)

type resolveContext struct {
	payload map[string]any
	item    any
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

type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) BuildRequest(vendorCfg *port.VendorConfig, mappingCfg *port.MappingConfig, payload map[string]any) (*http.Request, error) {
	if vendorCfg == nil || mappingCfg == nil {
		return nil, nil
	}

	ctx := resolveContext{payload: payload}

	bodyReader, err := e.buildRequestBody(mappingCfg, ctx)
	if err != nil {
		return nil, err
	}

	resolvedPath, err := e.resolveField(mappingCfg.Request.Path, ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	urlStr := vendorCfg.BaseURL + tostring(resolvedPath)

	req, err := http.NewRequest(mappingCfg.Request.Method, urlStr, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	for k, v := range mappingCfg.Request.Headers {
		req.Header.Set(k, v)
	}

	injectAuth(req, vendorCfg)
	return req, nil
}

func (e *Engine) buildRequestBody(mappingCfg *port.MappingConfig, ctx resolveContext) (io.Reader, error) {
	if mappingCfg.Body.Type == "" || mappingCfg.Body.Type == bodyTypeNone {
		return nil, nil
	}

	bodyBytes, err := e.buildBody(&mappingCfg.Body, ctx)
	if err != nil {
		return nil, err
	}
	if len(bodyBytes) > 0 {
		return bytes.NewReader(bodyBytes), nil
	}
	return nil, nil
}

func injectAuth(req *http.Request, vendorCfg *port.VendorConfig) {
	if vendorCfg.Auth == nil {
		return
	}
	switch vendorCfg.Auth.Type {
	case authTypeBearer:
		if token, ok := authConfigString(vendorCfg.Auth.Config, authConfigKeyToken); ok {
			req.Header.Set(authHeaderName, authBearerPrefix+token)
		}
	case authTypeBasic:
		user, ok := authConfigString(vendorCfg.Auth.Config, authConfigKeyUser)
		if !ok {
			break
		}
		pass, ok := authConfigString(vendorCfg.Auth.Config, authConfigKeyPass)
		if !ok {
			break
		}
		req.Header.Set(authHeaderName, authBasicPrefix+user+":"+pass)
	}
}

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
		return e.resolveRawBody(string(tmplBytes), ctx)
	case bodyTypeMapping:
		return e.resolveMappingBody(bodyCfg.Template, ctx)
	case bodyTypePlugin:
		return nil, fmt.Errorf("mapper plugin not supported yet")
	default:
		return nil, fmt.Errorf("unknown body type: %s", bodyCfg.Type)
	}
}

func (e *Engine) resolveMappingBody(template any, ctx resolveContext) ([]byte, error) {
	resolved, err := e.resolveNode(template, ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resolved)
}

func (e *Engine) resolveRawBody(template string, ctx resolveContext) ([]byte, error) {
	resolved, err := e.resolveField(template, ctx)
	if err != nil {
		return nil, err
	}
	return []byte(tostring(resolved)), nil
}

func (e *Engine) resolveNode(node any, ctx resolveContext) (any, error) {
	switch v := node.(type) {
	case string:
		return e.resolveField(v, ctx)
	case map[string]any:
		if _, ok := v[port.DirectiveSource]; ok {
			return e.resolveSourceDirective(v, ctx)
		}
		result := make(map[string]any, len(v))
		for key, val := range v {
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
		return v, nil
	}
}

func (e *Engine) resolveSourceDirective(v map[string]any, ctx resolveContext) (any, error) {
	if _, ok := v[port.DirectiveEach]; ok {
		return e.resolveEachDirective(v, ctx)
	}

	sourceExpr, ok := v[port.DirectiveSource].(string)
	if !ok {
		return nil, fmt.Errorf("$source must be a string")
	}

	raw, err := e.resolveField(sourceExpr, ctx)
	if err != nil {
		return nil, err
	}

	raw, err = applyTypeConversion(raw, v)
	if err != nil {
		return nil, err
	}

	return applyFormatConversion(raw, v)
}

func applyTypeConversion(raw any, v map[string]any) (any, error) {
	if typeNameVal, ok := v[port.DirectiveType]; ok {
		typeName, ok := typeNameVal.(string)
		if !ok {
			return nil, fmt.Errorf("$type must be a string")
		}
		return convertType(raw, typeName)
	}
	return raw, nil
}

func applyFormatConversion(raw any, v map[string]any) (any, error) {
	if formatVal, ok := v[port.DirectiveFormat]; ok {
		format, ok := formatVal.(string)
		if !ok {
			return nil, fmt.Errorf("$format must be a string")
		}
		return formatValue(tostring(raw), format)
	}
	return raw, nil
}

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

func (e *Engine) resolveField(expr string, ctx resolveContext) (any, error) {
	loc := resolveRefRe.FindStringIndex(expr)
	if loc == nil {
		return expr, nil
	}

	if loc[0] == 0 && loc[1] == len(expr) {
		matches := resolveRefRe.FindStringSubmatch(expr)
		scope := matches[1]
		path := matches[2]
		if path == "" {
			return e.getScopeValue(scope, ctx), nil
		}
		return getNestedField(ctx.lookup(scope), path), nil
	}

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

func convertType(val any, typeName string) (any, error) {
	switch typeName {
	case typeNameString:
		return tostring(val), nil
	case typeNameInteger:
		return convertToInteger(val)
	case typeNameNumber:
		return convertToNumber(val)
	case typeNameBoolean:
		return convertToBoolean(val)
	default:
		return nil, fmt.Errorf("unknown type: %s", typeName)
	}
}

func convertToInteger(val any) (any, error) {
	if n, ok := toFloat64(val); ok {
		return int64(n), nil
	}
	if s, ok := val.(string); ok {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot convert %q to integer", s)
		}
		return n, nil
	}
	return nil, fmt.Errorf("cannot convert %T to integer", val)
}

func convertToNumber(val any) (any, error) {
	switch v := val.(type) {
	case int, int64:
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
}

func convertToBoolean(val any) (any, error) {
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
}

func formatValue(val, format string) (string, error) {
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return "", fmt.Errorf("cannot parse %q as timestamp: %w", val, err)
	}
	return time.Unix(n, 0).UTC().Format(format), nil
}

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

func authConfigString(cfg map[string]any, key string) (string, bool) {
	v, ok := cfg[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func toFloat64(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}
