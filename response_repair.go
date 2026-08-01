package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type argumentTypeIssue struct {
	Tool   string `json:"tool"`
	Path   string `json:"path"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type toolRepairDelta struct {
	CallsInspected int
	NamesRestored  int
	FieldsFixed    int
	Issues         int
}

type responseRepairReport struct {
	Changed         bool
	ActualRestored  int
	ActualFixes     []argumentFieldFix
	DiagnosticDelta map[string]*toolRepairDelta
	Issues          []argumentTypeIssue
}

func newResponseRepairReport() responseRepairReport {
	return responseRepairReport{DiagnosticDelta: make(map[string]*toolRepairDelta)}
}

func (report *responseRepairReport) tool(tool string) *toolRepairDelta {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "(unknown)"
	}
	delta := report.DiagnosticDelta[tool]
	if delta == nil {
		delta = &toolRepairDelta{}
		report.DiagnosticDelta[tool] = delta
	}
	return delta
}

func repairStrictResponse(raw []byte, sourceFormat string, state *strictRequestState) ([]byte, responseRepairReport) {
	report := newResponseRepairReport()
	if state == nil || len(raw) == 0 {
		return raw, report
	}
	format := strings.ToLower(strings.TrimSpace(sourceFormat))
	if format != "openai" && format != "openai-response" {
		return raw, report
	}

	transform := func(payload []byte) ([]byte, bool) {
		value, errDecode := decodeJSONValue(string(payload))
		if errDecode != nil {
			return payload, false
		}
		root, ok := value.(map[string]any)
		if !ok {
			return payload, false
		}
		changed := repairResponseObject(root, state, &report)
		if !changed {
			return payload, false
		}
		updated, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			return payload, false
		}
		return updated, true
	}

	if json.Valid(raw) {
		updated, changed := transform(raw)
		report.Changed = changed
		return updated, report
	}

	lines := bytes.SplitAfter(raw, []byte("\n"))
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
		updated, payloadChanged := transform(payload)
		if !payloadChanged {
			continue
		}
		rebuilt := make([]byte, 0, prefixLength+1+len(updated)+len(ending))
		rebuilt = append(rebuilt, content[:prefixLength]...)
		rebuilt = append(rebuilt, ' ')
		rebuilt = append(rebuilt, updated...)
		rebuilt = append(rebuilt, ending...)
		lines[index] = rebuilt
		changed = true
	}
	report.Changed = changed
	if !changed {
		return raw, report
	}
	return bytes.Join(lines, nil), report
}

func repairResponseObject(root map[string]any, state *strictRequestState, report *responseRepairReport) bool {
	changed := false
	if eventType, _ := root["type"].(string); eventType == "response.function_call_arguments.done" {
		itemID, _ := root["item_id"].(string)
		tool := state.itemTool(itemID)
		if tool != "" {
			if repairFunctionArguments(root, "arguments", tool, itemID, state, report) {
				changed = true
			}
		}
	}
	if walkResponseObjects(root, state, report) {
		changed = true
	}
	return changed
}

func walkResponseObjects(value any, state *strictRequestState, report *responseRepairReport) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if walkResponseObjects(item, state, report) {
				changed = true
			}
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if itemType == "function_call" {
			if repairResponsesFunctionCall(typed, state, report) {
				changed = true
			}
		}
		if function, ok := typed["function"].(map[string]any); ok {
			_, hasArguments := function["arguments"]
			_, hasCallID := typed["id"]
			if hasArguments || hasCallID {
				if repairChatFunctionCall(typed, function, state, report) {
					changed = true
				}
			}
		}
		for _, child := range typed {
			if walkResponseObjects(child, state, report) {
				changed = true
			}
		}
	}
	return changed
}

func repairChatFunctionCall(call, function map[string]any, state *strictRequestState, report *responseRepairReport) bool {
	name, _ := function["name"].(string)
	callID, _ := call["id"].(string)
	changed, clientName := restoreResponseToolName(function, "name", name, callID, state, report)
	if clientName == "" {
		clientName = name
	}
	if repairFunctionArguments(function, "arguments", clientName, callID, state, report) {
		changed = true
	}
	return changed
}

func repairResponsesFunctionCall(item map[string]any, state *strictRequestState, report *responseRepairReport) bool {
	name, _ := item["name"].(string)
	itemID, _ := item["id"].(string)
	changed, clientName := restoreResponseToolName(item, "name", name, itemID, state, report)
	if clientName == "" {
		clientName = name
	}
	state.rememberItemTool(itemID, clientName)
	if repairFunctionArguments(item, "arguments", clientName, itemID, state, report) {
		changed = true
	}
	return changed
}

func restoreResponseToolName(target map[string]any, field, name, callID string, state *strictRequestState, report *responseRepairReport) (bool, string) {
	clientName, mapped := state.CanonicalToClient[name]
	if !mapped || clientName == "" || clientName == name {
		return false, name
	}
	target[field] = clientName
	report.ActualRestored++
	key := stableCallKey(callID, clientName, "") + "|restore"
	if state.claimRestore(key) {
		report.tool(clientName).NamesRestored++
	}
	return true, clientName
}

func repairFunctionArguments(target map[string]any, field, tool, callID string, state *strictRequestState, report *responseRepairReport) bool {
	rawArguments, exists := target[field]
	if !exists {
		return false
	}
	argumentsText, ok := rawArguments.(string)
	if !ok || strings.TrimSpace(argumentsText) == "" {
		return false
	}
	schema, schemaFound := state.Schemas[tool]
	if !schemaFound {
		return false
	}
	callKey := stableCallKey(callID, tool, argumentsText)
	if state.claimCall(callKey) {
		report.tool(tool).CallsInspected++
	}
	arguments, errDecode := decodeJSONValue(argumentsText)
	if errDecode != nil {
		issue := argumentTypeIssue{Tool: tool, Path: "$", From: "string", To: "object", Reason: "invalid_arguments_json"}
		recordArgumentIssue(state, report, callKey, issue)
		return false
	}
	normalized, fixes, issues := normalizeStrictSchemaValueDetailed(arguments, schema, tool, "")
	for _, issue := range issues {
		recordArgumentIssue(state, report, callKey, issue)
	}
	if len(fixes) == 0 {
		return false
	}
	updatedArguments, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		issue := argumentTypeIssue{Tool: tool, Path: "$", From: jsonTypeName(normalized), To: "object", Reason: "encode_failed"}
		recordArgumentIssue(state, report, callKey, issue)
		return false
	}
	target[field] = string(updatedArguments)
	report.ActualFixes = append(report.ActualFixes, fixes...)
	for _, fix := range fixes {
		if state.claimFix(callKey + "|" + fix.Kind + "|" + fix.Path + "|" + fix.From + "|" + fix.To) {
			report.tool(tool).FieldsFixed++
		}
	}
	return true
}

func recordArgumentIssue(state *strictRequestState, report *responseRepairReport, callKey string, issue argumentTypeIssue) {
	key := callKey + "|" + issue.Path + "|" + issue.From + "|" + issue.To + "|" + issue.Reason
	if !state.claimIssue(key) {
		return
	}
	report.Issues = append(report.Issues, issue)
	report.tool(issue.Tool).Issues++
}

func stableCallKey(callID, tool, arguments string) string {
	if strings.TrimSpace(callID) != "" {
		return strings.TrimSpace(callID)
	}
	sum := sha256.Sum256([]byte(tool + "\x00" + arguments))
	return "sha256:" + hex.EncodeToString(sum[:8])
}
