package ingestion

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
)

const validationRootPath = "payload"

type schemaValidator struct {
	mu    sync.Mutex
	cache map[uintptr]*schemaNode
}

func newSchemaValidator() *schemaValidator {
	return &schemaValidator{
		cache: make(map[uintptr]*schemaNode),
	}
}

func (v *schemaValidator) validate(schemaDef []byte, payload map[string]any) []ValidationError {
	var schema schemaNode
	if err := json.Unmarshal(schemaDef, &schema); err != nil {
		return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
	}
	return validateNode(schema, payload, validationRootPath)
}

func (v *schemaValidator) validateMap(schemaMap map[string]any, payload map[string]any) []ValidationError {
	key := reflect.ValueOf(schemaMap).Pointer()

	v.mu.Lock()
	node, ok := v.cache[key]
	v.mu.Unlock()

	if !ok {
		schemaJSON, err := json.Marshal(schemaMap)
		if err != nil {
			return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
		}
		var parsed schemaNode
		if err := json.Unmarshal(schemaJSON, &parsed); err != nil {
			return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
		}
		node = &parsed
		v.mu.Lock()
		v.cache[key] = node
		v.mu.Unlock()
	}

	return validateNode(*node, payload, validationRootPath)
}

type schemaNode struct {
	Type       string                 `json:"type"`
	Required   []string               `json:"required"`
	Properties map[string]schemaNode  `json:"properties"`
	Items      *schemaNode            `json:"items"`
	Enum       []any                  `json:"enum"`
	Minimum    *float64               `json:"minimum"`
	Maximum    *float64               `json:"maximum"`
	MinLength  *int                   `json:"minLength"`
	MaxLength  *int                   `json:"maxLength"`
}

func validateNode(schema schemaNode, value any, path string) []ValidationError {
	if value == nil {
		return nil
	}

	switch schema.Type {
	case "object":
		return validateObjectNode(schema, value, path)
	case "array":
		return validateArrayNode(schema, value, path)
	case "string":
		return validateStringNode(schema, value, path)
	case "integer":
		return validateIntegerNode(schema, value, path)
	case "number":
		return validateNumberNode(value, path)
	case "boolean":
		return validateBooleanNode(value, path)
	default:
		return nil
	}
}

func validateObjectNode(schema schemaNode, value any, path string) []ValidationError {
	m, ok := value.(map[string]any)
	if !ok {
		return []ValidationError{typeMismatch(path, "object", value)}
	}

	var errs []ValidationError
	for _, req := range schema.Required {
		if _, exists := m[req]; !exists || m[req] == nil {
			errs = append(errs, ValidationError{
				Field:   path + "." + req,
				Message: "required field missing",
			})
		}
	}
	for key, propSchema := range schema.Properties {
		if val, ok := m[key]; ok && val != nil {
			childErrs := validateNode(propSchema, val, path+"."+key)
			errs = append(errs, childErrs...)
		}
	}
	return errs
}

func validateArrayNode(schema schemaNode, value any, path string) []ValidationError {
	arr, ok := value.([]any)
	if !ok {
		return []ValidationError{typeMismatch(path, "array", value)}
	}

	var errs []ValidationError
	if schema.Items != nil {
		for i, item := range arr {
			childErrs := validateNode(*schema.Items, item, fmt.Sprintf("%s[%d]", path, i))
			errs = append(errs, childErrs...)
		}
	}
	return errs
}

func validateStringNode(schema schemaNode, value any, path string) []ValidationError {
	if _, ok := value.(string); !ok {
		return []ValidationError{typeMismatch(path, "string", value)}
	}
	if len(schema.Enum) == 0 {
		return nil
	}

	strVal, ok := value.(string)
	if !ok {
		return nil
	}
	for _, e := range schema.Enum {
		if e == strVal {
			return nil
		}
	}
	return []ValidationError{{
		Field:   path,
		Message: fmt.Sprintf("value %q not in enum %v", strVal, schema.Enum),
	}}
}

func validateIntegerNode(schema schemaNode, value any, path string) []ValidationError {
	switch v := value.(type) {
	case float64:
		if v != float64(int64(v)) {
			return []ValidationError{{
				Field:   path,
				Message: fmt.Sprintf("expected integer, got float"),
			}}
		}
		if schema.Minimum != nil && v < *schema.Minimum {
			return []ValidationError{{
				Field:   path,
				Message: fmt.Sprintf("value %v is less than minimum %v", v, *schema.Minimum),
			}}
		}
		if schema.Maximum != nil && v > *schema.Maximum {
			return []ValidationError{{
				Field:   path,
				Message: fmt.Sprintf("value %v is greater than maximum %v", v, *schema.Maximum),
			}}
		}
		return nil
	case int, int64:
		return nil
	default:
		return []ValidationError{typeMismatch(path, "integer", value)}
	}
}

func validateNumberNode(value any, path string) []ValidationError {
	switch value.(type) {
	case float64, int, int64:
		return nil
	default:
		return []ValidationError{typeMismatch(path, "number", value)}
	}
}

func validateBooleanNode(value any, path string) []ValidationError {
	if _, ok := value.(bool); !ok {
		return []ValidationError{typeMismatch(path, "boolean", value)}
	}
	return nil
}

func typeMismatch(path, want string, got any) ValidationError {
	return ValidationError{Field: path, Message: fmt.Sprintf("expected %s, got %T", want, got)}
}
