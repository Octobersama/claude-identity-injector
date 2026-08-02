package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

type argumentFieldFix struct {
	Tool string
	Path string
	From string
	To   string
	Kind string
}

const (
	argumentFixKindType      = "type"
	argumentFixKindSchemaKey = "schema_key"
)

func toolSchemasFromRequest(rawRequests ...[]byte) map[string]map[string]any {
	result := make(map[string]map[string]any)
	for _, raw := range rawRequests {
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body == nil {
			continue
		}
		readToolSchemas(result, body["tools"])
	}
	return result
}

func readToolSchemas(result map[string]map[string]any, raw any) {
	tools, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if schema, ok := tool["parameters"].(map[string]any); ok && name != "" {
			result[name] = schema
		}
		if schema, ok := tool["input_schema"].(map[string]any); ok && name != "" {
			result[name] = schema
		}

		function, _ := tool["function"].(map[string]any)
		if function == nil {
			continue
		}
		name, _ = function["name"].(string)
		if schema, ok := function["parameters"].(map[string]any); ok && name != "" {
			result[name] = schema
		}
	}
}

func splitLineEnding(line []byte) ([]byte, []byte) {
	content := line
	ending := []byte{}
	if bytes.HasSuffix(content, []byte("\n")) {
		ending = []byte("\n")
		content = content[:len(content)-1]
	}
	if bytes.HasSuffix(content, []byte("\r")) {
		ending = append([]byte("\r"), ending...)
		content = content[:len(content)-1]
	}
	return content, ending
}

func decodeJSONValue(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, errDecode
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		if errTrailing == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, errTrailing
	}
	return value, nil
}

func normalizeStrictSchemaValueDetailed(value any, schema map[string]any, tool, path string) (any, []argumentFieldFix, []argumentTypeIssue) {
	return normalizeSchemaValueDetailedWithKeyRestore(value, schema, tool, path, true)
}

func normalizeSchemaValueDetailedWithKeyRestore(value any, schema map[string]any, tool, path string, restoreKeys bool) (any, []argumentFieldFix, []argumentTypeIssue) {
	typeName, ok := schemaNonAmbiguousType(schema)
	if !ok {
		return value, nil, nil
	}
	var fixes []argumentFieldFix
	var issues []argumentTypeIssue
	conversionFailed := false
	if _, isString := value.(string); isString && typeName != "string" && supportedRepairType(typeName) {
		converted, convertedOK, reason := convertStringValueDetailed(value, typeName)
		if convertedOK {
			value = converted
			fixes = append(fixes, argumentFieldFix{Tool: tool, Path: pathOrRoot(path), From: "string", To: typeName, Kind: argumentFixKindType})
		} else {
			conversionFailed = true
			issues = append(issues, argumentTypeIssue{Tool: tool, Path: pathOrRoot(path), From: "string", To: typeName, Reason: reason})
		}
	}
	if supportedRepairType(typeName) && !schemaTypeMatches(value, typeName) {
		if !conversionFailed {
			issues = append(issues, argumentTypeIssue{Tool: tool, Path: pathOrRoot(path), From: jsonTypeName(value), To: typeName, Reason: "type_mismatch"})
		}
		return value, fixes, issues
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if restoreKeys {
			keyFixes, keyIssues := restoreSchemaObjectKeys(typed, properties, tool, path)
			fixes = append(fixes, keyFixes...)
			issues = append(issues, keyIssues...)
		}
		for key, child := range typed {
			propertySchema, ok := properties[key].(map[string]any)
			if !ok {
				continue
			}
			normalizedChild, childFixes, childIssues := normalizeSchemaValueDetailedWithKeyRestore(child, propertySchema, tool, joinPath(path, key), restoreKeys)
			if len(childFixes) > 0 {
				typed[key] = normalizedChild
				fixes = append(fixes, childFixes...)
			}
			issues = append(issues, childIssues...)
		}
	case []any:
		itemSchema, _ := schema["items"].(map[string]any)
		if itemSchema == nil {
			break
		}
		for index, child := range typed {
			normalizedChild, childFixes, childIssues := normalizeSchemaValueDetailedWithKeyRestore(child, itemSchema, tool, joinPath(path, strconv.Itoa(index)), restoreKeys)
			if len(childFixes) > 0 {
				typed[index] = normalizedChild
				fixes = append(fixes, childFixes...)
			}
			issues = append(issues, childIssues...)
		}
	}
	return value, fixes, issues
}

func restoreSchemaObjectKeys(value, properties map[string]any, tool, path string) ([]argumentFieldFix, []argumentTypeIssue) {
	if len(value) == 0 || len(properties) == 0 {
		return nil, nil
	}

	propertiesByAlias := make(map[string][]string)
	for property := range properties {
		alias := schemaKeyAlias(property)
		if alias != "" {
			propertiesByAlias[alias] = append(propertiesByAlias[alias], property)
		}
	}
	for alias := range propertiesByAlias {
		sort.Strings(propertiesByAlias[alias])
	}

	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pending := make(map[string][]string)
	var issues []argumentTypeIssue
	for _, key := range keys {
		if _, exact := properties[key]; exact {
			continue
		}
		matches := propertiesByAlias[schemaKeyAlias(key)]
		switch len(matches) {
		case 0:
			continue
		case 1:
			pending[matches[0]] = append(pending[matches[0]], key)
		default:
			issues = append(issues, argumentTypeIssue{
				Tool: tool, Path: pathOrRoot(joinPath(path, key)), From: key,
				To: strings.Join(matches, "|"), Reason: "schema_key_ambiguous",
			})
		}
	}

	targets := make([]string, 0, len(pending))
	for target := range pending {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	var fixes []argumentFieldFix
	for _, target := range targets {
		sources := pending[target]
		sort.Strings(sources)
		_, targetExists := value[target]
		if targetExists || len(sources) != 1 {
			for _, source := range sources {
				issues = append(issues, argumentTypeIssue{
					Tool: tool, Path: pathOrRoot(joinPath(path, source)), From: source,
					To: target, Reason: "schema_key_conflict",
				})
			}
			continue
		}

		source := sources[0]
		value[target] = value[source]
		delete(value, source)
		fixes = append(fixes, argumentFieldFix{
			Tool: tool, Path: pathOrRoot(joinPath(path, target)), From: source,
			To: target, Kind: argumentFixKindSchemaKey,
		})
	}
	return fixes, issues
}

func schemaKeyAlias(key string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(key)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			normalized.WriteRune(character)
			continue
		}
		if character != '_' && character != '-' {
			return ""
		}
	}
	return normalized.String()
}

func schemaNonAmbiguousType(schema map[string]any) (string, bool) {
	if typeName, ok := schema["type"].(string); ok {
		return typeName, true
	}
	types, ok := schema["type"].([]any)
	if !ok || len(types) != 1 {
		return "", false
	}
	typeName, ok := types[0].(string)
	return typeName, ok
}

func convertStringValue(value any, target string) (any, bool) {
	converted, ok, _ := convertStringValueDetailed(value, target)
	return converted, ok
}

func convertStringValueDetailed(value any, target string) (any, bool, string) {
	raw, ok := value.(string)
	if !ok || target == "string" {
		return nil, false, "not_convertible"
	}
	if !json.Valid([]byte(raw)) {
		return nil, false, "invalid_json_string"
	}
	decoded, errDecode := decodeJSONValue(raw)
	if errDecode != nil {
		return nil, false, "invalid_json_string"
	}
	switch target {
	case "array":
		_, ok = decoded.([]any)
	case "object":
		_, ok = decoded.(map[string]any)
	case "boolean":
		_, ok = decoded.(bool)
	case "integer":
		number, isNumber := decoded.(json.Number)
		ok = isNumber && !strings.ContainsAny(number.String(), ".eE")
	case "number":
		_, ok = decoded.(json.Number)
	default:
		ok = false
	}
	if !ok {
		return nil, false, "decoded_type_mismatch"
	}
	return decoded, true, ""
}

func supportedRepairType(typeName string) bool {
	switch typeName {
	case "array", "object", "boolean", "integer", "number":
		return true
	default:
		return false
	}
}

func schemaTypeMatches(value any, typeName string) bool {
	switch typeName {
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && !strings.ContainsAny(number.String(), ".eE")
	case "number":
		_, ok := value.(json.Number)
		return ok
	default:
		return true
	}
}

func jsonTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return "number"
		}
		return "integer"
	case float64, float32:
		return "number"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func summarizeArgumentFixes(fixes []argumentFieldFix) []map[string]string {
	result := make([]map[string]string, 0, len(fixes))
	for _, fix := range fixes {
		result = append(result, map[string]string{"tool": fix.Tool, "path": fix.Path, "from": fix.From, "to": fix.To, "kind": fix.Kind})
	}
	return result
}
