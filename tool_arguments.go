package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

type argumentTypeFix struct {
	Tool string
	Path string
	From string
	To   string
}

func handleResponseNormalizeAfter(raw []byte) ([]byte, error) {
	var req responseTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if !currentConfig().Active || !strings.EqualFold(req.FromFormat, "claude") || !strings.EqualFold(req.ToFormat, "openai") {
		return okEnvelope(payloadResponse{})
	}

	schemas := toolSchemasFromRequest(req.OriginalRequest, req.TranslatedRequest)
	if len(schemas) == 0 {
		return okEnvelope(payloadResponse{})
	}
	updated, fixes := normalizeToolArguments(req.Body, schemas)
	if len(fixes) == 0 {
		return okEnvelope(payloadResponse{})
	}
	counters.toolArgumentsFixed.Add(uint64(len(fixes)))
	logHost("", "info", "Claude tool argument types normalized", map[string]any{
		"from_format": req.FromFormat,
		"to_format":   req.ToFormat,
		"model":       req.Model,
		"stream":      req.Stream,
		"fixes":       summarizeArgumentFixes(fixes),
	})
	return okEnvelope(payloadResponse{Body: updated})
}

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

func normalizeToolArguments(raw []byte, schemas map[string]map[string]any) ([]byte, []argumentTypeFix) {
	if json.Valid(raw) {
		return normalizeToolArgumentsJSON(raw, schemas)
	}

	lines := bytes.SplitAfter(raw, []byte("\n"))
	var fixes []argumentTypeFix
	changed := false
	for index, line := range lines {
		content, ending := splitLineEnding(line)
		trimmed := bytes.TrimLeft(content, " \t")
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		prefixLength := len(content) - len(trimmed) + len("data:")
		payload := bytes.TrimSpace(content[prefixLength:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
			continue
		}
		updated, lineFixes := normalizeToolArgumentsJSON(payload, schemas)
		if len(lineFixes) == 0 {
			continue
		}
		rebuilt := make([]byte, 0, prefixLength+1+len(updated)+len(ending))
		rebuilt = append(rebuilt, content[:prefixLength]...)
		rebuilt = append(rebuilt, ' ')
		rebuilt = append(rebuilt, updated...)
		rebuilt = append(rebuilt, ending...)
		lines[index] = rebuilt
		fixes = append(fixes, lineFixes...)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return bytes.Join(lines, nil), fixes
}

func normalizeToolArgumentsJSON(raw []byte, schemas map[string]map[string]any) ([]byte, []argumentTypeFix) {
	value, errDecode := decodeJSONValue(string(raw))
	if errDecode != nil {
		return raw, nil
	}
	fixes := normalizeToolArgumentsValue(value, schemas)
	if len(fixes) == 0 {
		return raw, nil
	}
	updated, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return raw, nil
	}
	return updated, fixes
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

func normalizeToolArgumentsValue(value any, schemas map[string]map[string]any) []argumentTypeFix {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	choices, ok := root["choices"].([]any)
	if !ok {
		return nil
	}

	var fixes []argumentTypeFix
	for _, choiceValue := range choices {
		choice, ok := choiceValue.(map[string]any)
		if !ok {
			continue
		}
		for _, messageKey := range []string{"message", "delta"} {
			message, ok := choice[messageKey].(map[string]any)
			if !ok {
				continue
			}
			fixes = append(fixes, normalizeToolCalls(message["tool_calls"], schemas)...)
		}
	}
	return fixes
}

func normalizeToolCalls(raw any, schemas map[string]map[string]any) []argumentTypeFix {
	toolCalls, ok := raw.([]any)
	if !ok {
		return nil
	}

	var fixes []argumentTypeFix
	for _, callValue := range toolCalls {
		call, ok := callValue.(map[string]any)
		if !ok {
			continue
		}
		function, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := function["name"].(string)
		rawArguments, ok := function["arguments"].(string)
		schema, schemaFound := schemas[name]
		if !ok || name == "" || !schemaFound {
			continue
		}
		arguments, errDecode := decodeJSONValue(rawArguments)
		if errDecode != nil {
			continue
		}
		normalizedArguments, argumentFixes := normalizeSchemaValue(arguments, schema, name, "")
		if len(argumentFixes) == 0 {
			continue
		}
		updatedArguments, errMarshal := json.Marshal(normalizedArguments)
		if errMarshal != nil {
			continue
		}
		function["arguments"] = string(updatedArguments)
		fixes = append(fixes, argumentFixes...)
	}
	return fixes
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

func normalizeSchemaValue(value any, schema map[string]any, tool, path string) (any, []argumentTypeFix) {
	normalized, fixes, _ := normalizeSchemaValueDetailed(value, schema, tool, path)
	return normalized, fixes
}

func normalizeSchemaValueDetailed(value any, schema map[string]any, tool, path string) (any, []argumentTypeFix, []argumentTypeIssue) {
	typeName, ok := schemaNonAmbiguousType(schema)
	if !ok {
		return value, nil, nil
	}
	var fixes []argumentTypeFix
	var issues []argumentTypeIssue
	conversionFailed := false
	if _, isString := value.(string); isString && typeName != "string" && supportedRepairType(typeName) {
		converted, convertedOK, reason := convertStringValueDetailed(value, typeName)
		if convertedOK {
			value = converted
			fixes = append(fixes, argumentTypeFix{Tool: tool, Path: pathOrRoot(path), From: "string", To: typeName})
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
		for key, child := range typed {
			propertySchema, ok := properties[key].(map[string]any)
			if !ok {
				continue
			}
			normalizedChild, childFixes, childIssues := normalizeSchemaValueDetailed(child, propertySchema, tool, joinPath(path, key))
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
			normalizedChild, childFixes, childIssues := normalizeSchemaValueDetailed(child, itemSchema, tool, joinPath(path, strconv.Itoa(index)))
			if len(childFixes) > 0 {
				typed[index] = normalizedChild
				fixes = append(fixes, childFixes...)
			}
			issues = append(issues, childIssues...)
		}
	}
	return value, fixes, issues
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

func summarizeArgumentFixes(fixes []argumentTypeFix) []map[string]string {
	result := make([]map[string]string, 0, len(fixes))
	for _, fix := range fixes {
		result = append(result, map[string]string{"tool": fix.Tool, "path": fix.Path, "from": fix.From, "to": fix.To})
	}
	return result
}
