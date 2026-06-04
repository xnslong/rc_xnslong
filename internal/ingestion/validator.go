package ingestion

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
)

// schemaValidator validates payloads against JSON Schema definitions.
type schemaValidator struct {
	mu    sync.Mutex
	cache map[uintptr]*schemaNode // key: pointer to schemaMap
}

func newSchemaValidator() *schemaValidator {
	return &schemaValidator{
		cache: make(map[uintptr]*schemaNode),
	}
}

// validate checks the payload against the given JSON Schema bytes.
// Returns a list of validation errors, or nil if valid.
func (v *schemaValidator) validate(schemaDef []byte, payload map[string]any) []ValidationError {
	var schema schemaNode
	if err := json.Unmarshal(schemaDef, &schema); err != nil {
		return []ValidationError{{Field: "", Message: fmt.Sprintf("invalid schema definition: %v", err)}}
	}
	return validateNode(schema, payload, "payload")
}

// validateMap checks the payload against the given JSON Schema map.
// Uses a cache keyed on the schema map's pointer to avoid repeated parsing.
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

	return validateNode(*node, payload, "payload")
}

// schemaNode represents a simplified JSON Schema node for MVP validation.
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

// validateNode recursively validates a value against a schema node.
func validateNode(schema schemaNode, value any, path string) []ValidationError {
	var errs []ValidationError

	if value == nil {
		// nil handled by required check, only flag if the type is expected to be object/array
		return nil
	}

	switch schema.Type {
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected object, got %T", value),
			})
			return errs
		}
		// Check required fields
		for _, req := range schema.Required {
			if _, exists := m[req]; !exists || m[req] == nil {
				errs = append(errs, ValidationError{
					Field:   path + "." + req,
					Message: "required field missing",
				})
			}
		}
		// Validate each property
		for key, propSchema := range schema.Properties {
			if val, ok := m[key]; ok && val != nil {
				childErrs := validateNode(propSchema, val, path+"."+key)
				errs = append(errs, childErrs...)
			}
		}

	case "array":
		arr, ok := value.([]any)
		if !ok {
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected array, got %T", value),
			})
			return errs
		}
		if schema.Items != nil {
			for i, item := range arr {
				childErrs := validateNode(*schema.Items, item, fmt.Sprintf("%s[%d]", path, i))
				errs = append(errs, childErrs...)
			}
		}

	case "string":
		if _, ok := value.(string); !ok {
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected string, got %T", value),
			})
		}
		// Enum check for strings
		if len(schema.Enum) > 0 {
			if strVal, ok := value.(string); ok {
				found := false
				for _, e := range schema.Enum {
					if e == strVal {
						found = true
						break
					}
				}
				if !found {
					errs = append(errs, ValidationError{
						Field:   path,
						Message: fmt.Sprintf("value %q not in enum %v", strVal, schema.Enum),
					})
				}
			}
		}

	case "integer":
		switch v := value.(type) {
		case float64:
			if v != float64(int64(v)) {
				errs = append(errs, ValidationError{
					Field:   path,
					Message: fmt.Sprintf("expected integer, got float"),
				})
			} else {
				// Check numeric constraints
				if schema.Minimum != nil && v < *schema.Minimum {
					errs = append(errs, ValidationError{
						Field:   path,
						Message: fmt.Sprintf("value %v is less than minimum %v", v, *schema.Minimum),
					})
				}
				if schema.Maximum != nil && v > *schema.Maximum {
					errs = append(errs, ValidationError{
						Field:   path,
						Message: fmt.Sprintf("value %v is greater than maximum %v", v, *schema.Maximum),
					})
				}
			}
		case int, int64:
			// Go's json.Unmarshal produces float64, but handle these just in case
		default:
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected integer, got %T", value),
			})
		}

	case "number":
		switch value.(type) {
		case float64, int, int64:
		default:
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected number, got %T", value),
			})
		}

	case "boolean":
		if _, ok := value.(bool); !ok {
			errs = append(errs, ValidationError{
				Field:   path,
				Message: fmt.Sprintf("expected boolean, got %T", value),
			})
		}
	}

	return errs
}
