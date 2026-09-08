package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/whyrusleeping/gollama"
)

// validateToolArguments checks a call against the JSON schema advertised by the
// tool. Tool handlers receive ordinary encoding/json values, so validation uses
// the same representation rather than coercing malformed arguments into handler
// defaults.
func validateToolArguments(raw string, schema any) error {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Errorf("arguments must be a JSON object: %v", err)
	}
	if args == nil {
		return fmt.Errorf("arguments must be a JSON object, got null")
	}
	root, ok := toolSchemaMap(schema)
	if !ok {
		return nil
	}
	return validateSchemaValue("", args, root)
}

func toolSchemaMap(schema any) (map[string]any, bool) {
	switch s := schema.(type) {
	case gollama.ToolFunctionParams:
		return map[string]any{
			"type":       s.Type,
			"properties": s.Properties,
			"required":   s.Required,
		}, true
	case *gollama.ToolFunctionParams:
		if s == nil {
			return nil, false
		}
		return toolSchemaMap(*s)
	case map[string]any:
		return s, true
	default:
		// MCP and other callers may supply a typed JSON-schema value. Normalize it
		// through JSON so Registry validation covers those tools too.
		data, err := json.Marshal(schema)
		if err != nil {
			return nil, false
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, false
		}
		return m, true
	}
}

func validateSchemaValue(path string, value any, schema map[string]any) error {
	typeName, _ := schema["type"].(string)
	if typeName != "" && !valueHasSchemaType(value, typeName) {
		return fmt.Errorf("%s must be %s, got %s", argumentName(path), typeName, jsonTypeName(value))
	}

	if values, ok := schemaEnum(schema["enum"]); ok {
		matched := false
		for _, allowed := range values {
			if schemaValuesEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must be one of %s, got %s", argumentName(path), formatEnum(values), formatValue(value))
		}
	}

	if number, ok := jsonNumber(value); ok {
		minimum, hasMinimum := jsonNumber(schema["minimum"])
		maximum, hasMaximum := jsonNumber(schema["maximum"])
		if hasMinimum && number < minimum || hasMaximum && number > maximum {
			switch {
			case hasMinimum && hasMaximum:
				return fmt.Errorf("%s must be between %s and %s, got %s", argumentName(path), formatNumber(minimum), formatNumber(maximum), formatNumber(number))
			case hasMinimum:
				return fmt.Errorf("%s must be at least %s, got %s", argumentName(path), formatNumber(minimum), formatNumber(number))
			default:
				return fmt.Errorf("%s must be at most %s, got %s", argumentName(path), formatNumber(maximum), formatNumber(number))
			}
		}
	}

	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok { // already reported above when the schema declares a type
			return nil
		}
		for _, name := range schemaStrings(schema["required"]) {
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s is required", argumentName(joinArgumentPath(path, name)))
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, childValue := range object {
			childSchema, ok := schemaObject(properties[name])
			if !ok {
				continue
			}
			if err := validateSchemaValue(joinArgumentPath(path, name), childValue, childSchema); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return nil
		}
		itemSchema, ok := schemaObject(schema["items"])
		if !ok {
			return nil
		}
		for i, item := range array {
			if err := validateSchemaValue(fmt.Sprintf("%s[%d]", path, i), item, itemSchema); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueHasSchemaType(value any, want string) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		n, ok := jsonNumber(value)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n
	case "number":
		_, ok := jsonNumber(value)
		return ok
	case "null":
		return value == nil
	default:
		// An unknown schema type is not a declaration this validator can enforce.
		return true
	}
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		if _, ok := jsonNumber(value); ok {
			return "number"
		}
		return fmt.Sprintf("%T", value)
	}
}

func jsonNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func schemaObject(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func schemaStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func schemaEnum(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func schemaValuesEqual(a, b any) bool {
	if an, ok := jsonNumber(a); ok {
		if bn, ok := jsonNumber(b); ok {
			return an == bn
		}
	}
	return reflect.DeepEqual(a, b)
}

func joinArgumentPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func argumentName(path string) string {
	if path == "" {
		return "arguments"
	}
	return fmt.Sprintf("argument %q", path)
}

func formatEnum(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = formatValue(value)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatValue(value any) string {
	data, err := json.Marshal(value)
	if err == nil {
		return string(data)
	}
	return fmt.Sprint(value)
}

func formatNumber(value float64) string {
	return fmt.Sprintf("%g", value)
}
