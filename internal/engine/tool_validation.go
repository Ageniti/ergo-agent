package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func validateToolCall(tool *ToolDefinition, call ToolCall) (ToolCall, error) {
	prepared := call
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return call, toolValidationError(call, []string{"$: invalid JSON: " + err.Error()})
	}
	if call.Name == "edit" {
		if object, ok := value.(map[string]any); ok {
			if _, hasEdits := object["edits"]; !hasEdits {
				oldText, hasOld := object["oldText"]
				newText, hasNew := object["newText"]
				if hasOld && hasNew {
					object["edits"] = []any{map[string]any{"oldText": oldText, "newText": newText}}
					delete(object, "oldText")
					delete(object, "newText")
				}
			}
		}
	}
	value = coerceWithJSONSchema(value, tool.Parameters)
	errors := validateSchemaValue(tool.Parameters, value, "$", nil)
	if len(errors) > 0 {
		return call, toolValidationError(call, errors)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return call, err
	}
	prepared.Arguments = encoded
	return prepared, nil
}

func toolValidationError(call ToolCall, validationErrors []string) error {
	var received any
	_ = json.Unmarshal(call.Arguments, &received)
	formatted, _ := json.MarshalIndent(received, "", "  ")
	lines := make([]string, len(validationErrors))
	for i, item := range validationErrors {
		lines[i] = "  - " + item
	}
	return fmt.Errorf("Validation failed for tool %q:\n%s\n\nReceived arguments:\n%s", call.Name, strings.Join(lines, "\n"), formatted)
}

func validateSchemaValue(schema map[string]any, value any, path string, errors []string) []string {
	return validateSchemaValueAt(schema, schema, value, path, errors)
}

func validateSchemaValueAt(root, schema map[string]any, value any, path string, errors []string) []string {
	if reference, ok := schema["$ref"].(string); ok {
		if resolved := resolveLocalSchemaRef(root, reference); resolved != nil {
			return validateSchemaValueAt(root, resolved, value, path, errors)
		}
		return append(errors, path+": unresolved schema reference "+reference)
	}
	for _, raw := range schemaList(schema["allOf"]) {
		errors = validateSchemaValueAt(root, raw, value, path, errors)
	}
	if alternatives := schemaList(schema["anyOf"]); len(alternatives) > 0 {
		matched := false
		for _, alternative := range alternatives {
			if len(validateSchemaValueAt(root, alternative, value, path, nil)) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			errors = append(errors, path+": expected value matching at least one schema")
		}
	}
	if alternatives := schemaList(schema["oneOf"]); len(alternatives) > 0 {
		matched := 0
		for _, alternative := range alternatives {
			if len(validateSchemaValueAt(root, alternative, value, path, nil)) == 0 {
				matched++
			}
		}
		if matched != 1 {
			errors = append(errors, path+": expected value matching exactly one schema")
		}
	}
	if rawNot, ok := schema["not"].(map[string]any); ok && len(validateSchemaValueAt(root, rawNot, value, path, nil)) == 0 {
		errors = append(errors, path+": value matches a forbidden schema")
	}

	types := schemaTypes(schema["type"])
	typeName := ""
	if len(types) > 1 {
		matched := false
		for _, candidateType := range types {
			if matchesJSONType(value, candidateType) {
				matched = true
				typeName = candidateType
				break
			}
		}
		if !matched {
			return append(errors, path+": expected "+strings.Join(types, " or "))
		}
	}
	if len(types) == 1 {
		typeName = types[0]
	}
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return append(errors, path+": expected object")
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, required := range stringList(schema["required"]) {
			if _, exists := object[required]; !exists {
				errors = append(errors, path+"."+required+": required property is missing")
			}
		}
		for key, item := range object {
			rawProperty, exists := properties[key]
			if !exists {
				if additional, present := schema["additionalProperties"].(bool); present && !additional {
					errors = append(errors, path+"."+key+": unexpected property")
				} else if additional, ok := schema["additionalProperties"].(map[string]any); ok {
					errors = validateSchemaValueAt(root, additional, item, path+"."+key, errors)
				}
				continue
			}
			property, _ := rawProperty.(map[string]any)
			errors = validateSchemaValueAt(root, property, item, path+"."+key, errors)
		}
		if minimum, ok := schemaNumber(schema["minProperties"]); ok && float64(len(object)) < minimum {
			errors = append(errors, fmt.Sprintf("%s: expected at least %g properties", path, minimum))
		}
		if maximum, ok := schemaNumber(schema["maxProperties"]); ok && float64(len(object)) > maximum {
			errors = append(errors, fmt.Sprintf("%s: expected at most %g properties", path, maximum))
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return append(errors, path+": expected array")
		}
		if minimum := schemaInt(schema["minItems"]); minimum > 0 && len(items) < minimum {
			errors = append(errors, fmt.Sprintf("%s: expected at least %d items", path, minimum))
		}
		if maximum := schemaInt(schema["maxItems"]); maximum > 0 && len(items) > maximum {
			errors = append(errors, fmt.Sprintf("%s: expected at most %d items", path, maximum))
		}
		if schemaBool(schema["uniqueItems"]) {
			for index := range items {
				for previous := 0; previous < index; previous++ {
					if reflect.DeepEqual(items[index], items[previous]) {
						errors = append(errors, fmt.Sprintf("%s[%d]: duplicate array item", path, index))
					}
				}
			}
		}
		if itemSchemas := schemaList(schema["items"]); len(itemSchemas) > 0 {
			for index, item := range items {
				if index < len(itemSchemas) {
					errors = validateSchemaValueAt(root, itemSchemas[index], item, fmt.Sprintf("%s[%d]", path, index), errors)
				}
			}
		} else if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range items {
				errors = validateSchemaValueAt(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, index), errors)
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			errors = append(errors, path+": expected string")
			break
		}
		length := utf8.RuneCountInString(text)
		if minimum, ok := schemaNumber(schema["minLength"]); ok && float64(length) < minimum {
			errors = append(errors, fmt.Sprintf("%s: expected at least %g characters", path, minimum))
		}
		if maximum, ok := schemaNumber(schema["maxLength"]); ok && float64(length) > maximum {
			errors = append(errors, fmt.Sprintf("%s: expected at most %g characters", path, maximum))
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil || !compiled.MatchString(text) {
				errors = append(errors, path+": string does not match pattern "+pattern)
			}
		}
	case "number", "integer":
		if !isJSONNumber(value, typeName == "integer") {
			errors = append(errors, path+": expected "+typeName)
			break
		}
		number, _ := schemaNumber(value)
		if minimum, ok := schemaNumber(schema["minimum"]); ok && number < minimum {
			errors = append(errors, fmt.Sprintf("%s: expected number >= %g", path, minimum))
		}
		if maximum, ok := schemaNumber(schema["maximum"]); ok && number > maximum {
			errors = append(errors, fmt.Sprintf("%s: expected number <= %g", path, maximum))
		}
		if minimum, ok := schemaNumber(schema["exclusiveMinimum"]); ok && number <= minimum {
			errors = append(errors, fmt.Sprintf("%s: expected number > %g", path, minimum))
		}
		if maximum, ok := schemaNumber(schema["exclusiveMaximum"]); ok && number >= maximum {
			errors = append(errors, fmt.Sprintf("%s: expected number < %g", path, maximum))
		}
		if multiple, ok := schemaNumber(schema["multipleOf"]); ok && multiple != 0 {
			quotient := number / multiple
			if math.Abs(quotient-math.Round(quotient)) > 1e-9 {
				errors = append(errors, fmt.Sprintf("%s: expected a multiple of %g", path, multiple))
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			errors = append(errors, path+": expected boolean")
		}
	case "null":
		if value != nil {
			errors = append(errors, path+": expected null")
		}
	}
	if expected, exists := schema["const"]; exists && !reflect.DeepEqual(normalizeComparable(expected), normalizeComparable(value)) {
		errors = append(errors, path+": value does not match const")
	}
	if allowed, ok := schema["enum"].([]string); ok {
		text, valid := value.(string)
		if valid && !contains(allowed, text) {
			errors = append(errors, path+": expected one of "+strings.Join(allowed, ", "))
		}
	} else if rawAllowed, ok := schema["enum"].([]any); ok {
		matched := false
		for _, allowed := range rawAllowed {
			if reflect.DeepEqual(normalizeComparable(allowed), normalizeComparable(value)) {
				matched = true
			}
		}
		if !matched {
			errors = append(errors, path+": value is not in enum")
		}
	}
	return errors
}

func coerceWithJSONSchema(value any, schema map[string]any) any {
	for _, nested := range schemaList(schema["allOf"]) {
		value = coerceWithJSONSchema(value, nested)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, nested := range schemaList(schema[key]) {
			candidate := cloneJSONValue(value)
			candidate = coerceWithJSONSchema(candidate, nested)
			if len(validateSchemaValue(nested, candidate, "$", nil)) == 0 {
				value = candidate
				break
			}
		}
	}
	types := schemaTypes(schema["type"])
	unionAlreadyMatches := len(types) > 1 && anyTypeMatches(value, types)
	if len(types) > 0 && !unionAlreadyMatches {
		for _, typeName := range types {
			candidate, changed := coercePrimitive(value, typeName)
			if changed {
				value = candidate
				break
			}
		}
	}
	if contains(types, "object") {
		if object, ok := value.(map[string]any); ok {
			properties, _ := schema["properties"].(map[string]any)
			for key, rawProperty := range properties {
				if item, exists := object[key]; exists {
					if property, ok := rawProperty.(map[string]any); ok {
						object[key] = coerceWithJSONSchema(item, property)
					}
				}
			}
			if additional, ok := schema["additionalProperties"].(map[string]any); ok {
				for key, item := range object {
					if _, defined := properties[key]; !defined {
						object[key] = coerceWithJSONSchema(item, additional)
					}
				}
			}
		}
	}
	if contains(types, "array") {
		if items, ok := value.([]any); ok {
			if tuple := schemaList(schema["items"]); len(tuple) > 0 {
				for index := range items {
					if index < len(tuple) {
						items[index] = coerceWithJSONSchema(items[index], tuple[index])
					}
				}
			} else if itemSchema, ok := schema["items"].(map[string]any); ok {
				for index := range items {
					items[index] = coerceWithJSONSchema(items[index], itemSchema)
				}
			}
		}
	}
	return value
}

func coercePrimitive(value any, typeName string) (any, bool) {
	switch typeName {
	case "number":
		if value == nil {
			return json.Number("0"), true
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			if number, err := strconv.ParseFloat(text, 64); err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
				return json.Number(text), true
			}
		}
		if flag, ok := value.(bool); ok {
			if flag {
				return json.Number("1"), true
			}
			return json.Number("0"), true
		}
	case "integer":
		if value == nil {
			return json.Number("0"), true
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			if number, err := strconv.ParseFloat(text, 64); err == nil && math.Trunc(number) == number {
				return json.Number(strconv.FormatFloat(number, 'f', -1, 64)), true
			}
		}
		if flag, ok := value.(bool); ok {
			if flag {
				return json.Number("1"), true
			}
			return json.Number("0"), true
		}
	case "boolean":
		if value == nil {
			return false, true
		}
		if text, ok := value.(string); ok && (text == "true" || text == "false") {
			return text == "true", true
		}
		if number, ok := schemaNumber(value); ok && (number == 0 || number == 1) {
			return number == 1, true
		}
	case "string":
		if value == nil {
			return "", true
		}
		switch item := value.(type) {
		case json.Number:
			return item.String(), true
		case float64:
			return strconv.FormatFloat(item, 'f', -1, 64), true
		case bool:
			return strconv.FormatBool(item), true
		}
	case "null":
		if value == "" || value == false {
			return nil, true
		}
		if number, ok := schemaNumber(value); ok && number == 0 {
			return nil, true
		}
	}
	return value, false
}

func schemaTypes(value any) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	return stringList(value)
}

func schemaList(value any) []map[string]any {
	switch values := value.(type) {
	case []map[string]any:
		return values
	case []any:
		out := make([]map[string]any, 0, len(values))
		for _, value := range values {
			if schema, ok := value.(map[string]any); ok {
				out = append(out, schema)
			}
		}
		return out
	default:
		return nil
	}
}

func matchesJSONType(value any, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		return isJSONNumber(value, false)
	case "integer":
		return isJSONNumber(value, true)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func anyTypeMatches(value any, types []string) bool {
	for _, typeName := range types {
		if matchesJSONType(value, typeName) {
			return true
		}
	}
	return false
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		result, err := number.Float64()
		return result, err == nil
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint32:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}

func schemaBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func cloneJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var clone any
	if err := decoder.Decode(&clone); err != nil {
		return value
	}
	return clone
}

func normalizeComparable(value any) any {
	if number, ok := schemaNumber(value); ok {
		return number
	}
	switch item := value.(type) {
	case []any:
		out := make([]any, len(item))
		for index := range item {
			out[index] = normalizeComparable(item[index])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, value := range item {
			out[key] = normalizeComparable(value)
		}
		return out
	default:
		return value
	}
}

func resolveLocalSchemaRef(root map[string]any, reference string) map[string]any {
	if reference == "#" {
		return root
	}
	if !strings.HasPrefix(reference, "#/") {
		return nil
	}
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[token]
		if !ok {
			return nil
		}
	}
	resolved, _ := current.(map[string]any)
	return resolved
}

func stringList(value any) []string {
	if strings, ok := value.([]string); ok {
		return strings
	}
	var out []string
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func schemaInt(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	case json.Number:
		result, _ := strconv.Atoi(string(number))
		return result
	default:
		return 0
	}
}

func isJSONNumber(value any, integer bool) bool {
	switch number := value.(type) {
	case json.Number:
		if integer {
			_, err := number.Int64()
			return err == nil
		}
		_, err := number.Float64()
		return err == nil
	case float64:
		return !integer || number == float64(int64(number))
	case int, int64:
		return true
	default:
		return false
	}
}
