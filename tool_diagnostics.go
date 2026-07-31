package main

import (
	"sort"
	"sync"
)

type toolDiagnostic struct {
	Tool            string               `json:"tool"`
	CallsInspected  uint64               `json:"calls_inspected"`
	NamesRestored   uint64               `json:"names_restored"`
	FieldsFixed     uint64               `json:"fields_fixed"`
	UnfixableIssues uint64               `json:"unfixable_issues"`
	LastIssue       *toolDiagnosticIssue `json:"last_issue,omitempty"`
}

type toolDiagnosticIssue struct {
	Path   string `json:"path"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

var toolDiagnosticState = struct {
	sync.Mutex
	values map[string]*toolDiagnostic
}{values: make(map[string]*toolDiagnostic)}

func recordResponseRepair(callbackID, requestID, sourceFormat, model string, stream bool, report responseRepairReport) {
	if report.ActualRestored > 0 {
		counters.toolNamesRestored.Add(uint64(report.ActualRestored))
	}
	if len(report.ActualFixes) > 0 {
		counters.toolArgumentsFixed.Add(uint64(len(report.ActualFixes)))
	}

	if len(report.DiagnosticDelta) > 0 || len(report.Issues) > 0 {
		toolDiagnosticState.Lock()
		for tool, delta := range report.DiagnosticDelta {
			diagnostic := toolDiagnosticState.values[tool]
			if diagnostic == nil {
				diagnostic = &toolDiagnostic{Tool: tool}
				toolDiagnosticState.values[tool] = diagnostic
			}
			diagnostic.CallsInspected += uint64(delta.CallsInspected)
			diagnostic.NamesRestored += uint64(delta.NamesRestored)
			diagnostic.FieldsFixed += uint64(delta.FieldsFixed)
			diagnostic.UnfixableIssues += uint64(delta.Issues)
		}
		for _, issue := range report.Issues {
			diagnostic := toolDiagnosticState.values[issue.Tool]
			if diagnostic == nil {
				diagnostic = &toolDiagnostic{Tool: issue.Tool}
				toolDiagnosticState.values[issue.Tool] = diagnostic
			}
			diagnostic.LastIssue = &toolDiagnosticIssue{Path: issue.Path, From: issue.From, To: issue.To, Reason: issue.Reason}
		}
		toolDiagnosticState.Unlock()
	}

	if report.ActualRestored > 0 || len(report.ActualFixes) > 0 {
		logHost(callbackID, "info", "Claude strict client tool response repaired", map[string]any{
			"request_id":     requestID,
			"source_format":  sourceFormat,
			"model":          model,
			"stream":         stream,
			"names_restored": report.ActualRestored,
			"fixes":          summarizeArgumentFixes(report.ActualFixes),
		})
	}
	if len(report.Issues) > 0 {
		logHost(callbackID, "warn", "Claude strict client tool response contains arguments that cannot be safely repaired", map[string]any{
			"request_id":    requestID,
			"source_format": sourceFormat,
			"model":         model,
			"stream":        stream,
			"issues":        summarizeArgumentIssues(report.Issues),
		})
	}
}

func toolDiagnosticSnapshot() []toolDiagnostic {
	toolDiagnosticState.Lock()
	defer toolDiagnosticState.Unlock()
	result := make([]toolDiagnostic, 0, len(toolDiagnosticState.values))
	for _, value := range toolDiagnosticState.values {
		copyValue := *value
		if value.LastIssue != nil {
			copyIssue := *value.LastIssue
			copyValue.LastIssue = &copyIssue
		}
		result = append(result, copyValue)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tool < result[j].Tool })
	return result
}

func summarizeArgumentIssues(issues []argumentTypeIssue) []map[string]string {
	result := make([]map[string]string, 0, len(issues))
	for _, issue := range issues {
		result = append(result, map[string]string{
			"tool": issue.Tool, "path": issue.Path, "from": issue.From, "to": issue.To, "reason": issue.Reason,
		})
	}
	return result
}
