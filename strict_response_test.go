package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func installStrictTestState(t *testing.T, requestID string, schemas map[string]map[string]any, mapping map[string]string) *strictRequestState {
	t.Helper()
	state := &strictRequestState{
		CanonicalToClient: mapping,
		Schemas:           schemas,
		itemTools:         make(map[string]string),
		seenCalls:         make(map[string]struct{}),
		seenRestores:      make(map[string]struct{}),
		seenFixes:         make(map[string]struct{}),
		seenIssues:        make(map[string]struct{}),
	}
	strictRequests.Lock()
	strictRequests.values[requestID] = state
	strictRequests.Unlock()
	t.Cleanup(func() { deleteStrictRequest(requestID) })
	return state
}

func responseBodyFromEnvelope(t *testing.T, raw []byte) []byte {
	t.Helper()
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal(envelope) error = %v", errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("envelope = %s", raw)
	}
	return envelope.Result.Body
}

func schema(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties}
}

func TestFinalResponseInterceptorLeavesNonStrictRequestUnchanged(t *testing.T) {
	deleteStrictRequest("ordinary")
	req, _ := json.Marshal(responseInterceptRequest{
		RequestID:    "ordinary",
		SourceFormat: "openai",
		Body:         []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"TodoWrite","arguments":"{\"todos\":\"[]\"}"}}]}}]}`),
	})
	raw, errHandle := handleResponseIntercept(req)
	if errHandle != nil {
		t.Fatalf("handleResponseIntercept() error = %v", errHandle)
	}
	if body := responseBodyFromEnvelope(t, raw); len(body) != 0 {
		t.Fatalf("non-strict response body = %s", body)
	}
}

func TestFinalResponseInterceptorRepairsKnownOpenCodeFields(t *testing.T) {
	installStrictTestState(t, "known-fields", map[string]map[string]any{
		"todowrite": schema(map[string]any{
			"todos": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		}),
		"ast_grep_search": schema(map[string]any{
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}),
		"ast_grep_replace": schema(map[string]any{
			"paths":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"globs":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"dry_run": map[string]any{"type": "boolean"},
		}),
	}, nil)
	body := []byte(`{"choices":[{"message":{"tool_calls":[` +
		`{"id":"todo-1","type":"function","function":{"name":"todowrite","arguments":"{\"todos\":\"[{\\\"content\\\":\\\"x\\\"}]\"}"}},` +
		`{"id":"search-1","type":"function","function":{"name":"ast_grep_search","arguments":"{\"paths\":\"[\\\"a.go\\\"]\"}"}},` +
		`{"id":"replace-1","type":"function","function":{"name":"ast_grep_replace","arguments":"{\"paths\":\"[\\\"a.go\\\"]\",\"globs\":\"[\\\"*.go\\\"]\",\"dry_run\":\"false\"}"}}` +
		`]}}]}`)
	req, _ := json.Marshal(responseInterceptRequest{RequestID: "known-fields", SourceFormat: "openai", Body: body})
	raw, errHandle := handleResponseIntercept(req)
	if errHandle != nil {
		t.Fatalf("handleResponseIntercept() error = %v", errHandle)
	}
	updated := responseBodyFromEnvelope(t, raw)
	if len(updated) == 0 {
		t.Fatal("strict response was not repaired")
	}
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(updated, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal(response) error = %v", errUnmarshal)
	}
	for _, call := range response.Choices[0].Message.ToolCalls {
		var arguments map[string]any
		if errUnmarshal := json.Unmarshal([]byte(call.Function.Arguments), &arguments); errUnmarshal != nil {
			t.Fatalf("arguments for %s = %q: %v", call.Function.Name, call.Function.Arguments, errUnmarshal)
		}
		switch call.Function.Name {
		case "todowrite":
			if _, ok := arguments["todos"].([]any); !ok {
				t.Fatalf("todowrite.todos = %#v", arguments["todos"])
			}
		case "ast_grep_search":
			if _, ok := arguments["paths"].([]any); !ok {
				t.Fatalf("ast_grep_search.paths = %#v", arguments["paths"])
			}
		case "ast_grep_replace":
			if _, ok := arguments["paths"].([]any); !ok {
				t.Fatalf("ast_grep_replace.paths = %#v", arguments["paths"])
			}
			if _, ok := arguments["globs"].([]any); !ok {
				t.Fatalf("ast_grep_replace.globs = %#v", arguments["globs"])
			}
			if value, ok := arguments["dry_run"].(bool); !ok || value {
				t.Fatalf("ast_grep_replace.dry_run = %#v", arguments["dry_run"])
			}
		}
	}
}

func TestFinalStreamInterceptorRestoresNameAndPreservesSSEFraming(t *testing.T) {
	installStrictTestState(t, "chat-stream", map[string]map[string]any{
		"read_file": schema(map[string]any{"paths": map[string]any{"type": "array"}}),
	}, map[string]string{"Read": "read_file"})
	body := []byte("event: chat.completion.chunk\r\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-read\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"paths\\\":\\\"[]\\\"}\"}}]}}]}\r\n\r\n")
	req, _ := json.Marshal(streamChunkInterceptRequest{RequestID: "chat-stream", SourceFormat: "openai", ChunkIndex: 0, Body: body})
	raw, errHandle := handleStreamChunkIntercept(req)
	if errHandle != nil {
		t.Fatalf("handleStreamChunkIntercept() error = %v", errHandle)
	}
	updated := responseBodyFromEnvelope(t, raw)
	if !bytes.Contains(updated, []byte(`"name":"read_file"`)) || !bytes.Contains(updated, []byte(`"arguments":"{\"paths\":[]}"`)) {
		t.Fatalf("updated = %q", updated)
	}
	if !bytes.HasPrefix(updated, []byte("event: chat.completion.chunk\r\ndata: ")) || !bytes.HasSuffix(updated, []byte("\r\n\r\n")) {
		t.Fatalf("SSE framing changed: %q", updated)
	}
}

func TestResponsesStreamRepairsArgumentsDoneAndCompleteEvents(t *testing.T) {
	installStrictTestState(t, "responses-stream", map[string]map[string]any{
		"todowrite": schema(map[string]any{"todos": map[string]any{"type": "array"}}),
	}, nil)

	events := []string{
		`{"type":"response.output_item.added","item":{"id":"fc_call-1","type":"function_call","name":"todowrite","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_call-1","arguments":"{\"todos\":\"[]\"}"}`,
		`{"type":"response.output_item.done","item":{"id":"fc_call-1","type":"function_call","name":"todowrite","arguments":"{\"todos\":\"[]\"}"}}`,
		`{"type":"response.completed","response":{"output":[{"id":"fc_call-1","type":"function_call","name":"todowrite","arguments":"{\"todos\":\"[]\"}"}]}}`,
	}
	for index, event := range events {
		body := []byte("event: update\ndata: " + event + "\n\n")
		req, _ := json.Marshal(streamChunkInterceptRequest{RequestID: "responses-stream", SourceFormat: "openai-response", ChunkIndex: index, Body: body})
		raw, errHandle := handleStreamChunkIntercept(req)
		if errHandle != nil {
			t.Fatalf("event %d: handleStreamChunkIntercept() error = %v", index, errHandle)
		}
		updated := responseBodyFromEnvelope(t, raw)
		if index == 0 {
			if len(updated) != 0 {
				t.Fatalf("added event unexpectedly changed: %s", updated)
			}
			continue
		}
		if !bytes.Contains(updated, []byte(`"arguments":"{\"todos\":[]}"`)) {
			t.Fatalf("event %d not repaired: %q", index, updated)
		}
	}
}

func TestResponsesNonStreamRestoresToolNameAndRepairsArguments(t *testing.T) {
	installStrictTestState(t, "responses-json", map[string]map[string]any{
		"read_file": schema(map[string]any{"paths": map[string]any{"type": "array"}}),
	}, map[string]string{"Read": "read_file"})
	body := []byte(`{"id":"resp-1","output":[{"id":"fc-1","type":"function_call","name":"Read","arguments":"{\"paths\":\"[\\\"a.go\\\"]\"}"}]}`)
	req, _ := json.Marshal(responseInterceptRequest{RequestID: "responses-json", SourceFormat: "openai-response", Body: body})
	raw, errHandle := handleResponseIntercept(req)
	if errHandle != nil {
		t.Fatalf("handleResponseIntercept() error = %v", errHandle)
	}
	updated := responseBodyFromEnvelope(t, raw)
	if !bytes.Contains(updated, []byte(`"name":"read_file"`)) || !bytes.Contains(updated, []byte(`"arguments":"{\"paths\":[\"a.go\"]}"`)) {
		t.Fatalf("updated = %s", updated)
	}
}

func TestStrictResponseRejectsAmbiguousAndInvalidConversions(t *testing.T) {
	state := installStrictTestState(t, "invalid", map[string]map[string]any{
		"tool": schema(map[string]any{
			"ambiguous": map[string]any{"type": []any{"string", "array"}},
			"paths":     map[string]any{"type": "array"},
		}),
	}, nil)
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"bad-1","type":"function","function":{"name":"tool","arguments":"{\"ambiguous\":\"[]\",\"paths\":\"[] {}\"}"}}]}}]}`)
	updated, report := repairStrictResponse(body, "openai", state)
	if report.Changed || !bytes.Equal(updated, body) {
		t.Fatalf("invalid response changed: %s", updated)
	}
	if len(report.Issues) != 1 || report.Issues[0].Path != "paths" || report.Issues[0].Reason != "invalid_json_string" {
		t.Fatalf("issues = %#v", report.Issues)
	}
}

func TestStrictRequestLifecycleCleanupAndIsolation(t *testing.T) {
	installStrictTestState(t, "request-a", map[string]map[string]any{"tool": schema(map[string]any{"value": map[string]any{"type": "array"}})}, nil)
	installStrictTestState(t, "request-b", map[string]map[string]any{"tool": schema(map[string]any{"value": map[string]any{"type": "boolean"}})}, nil)
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"tool","arguments":"{\"value\":\"[]\"}"}}]}}]}`)

	updatedA, reportA := repairStrictResponse(body, "openai", strictRequest("request-a"))
	updatedB, reportB := repairStrictResponse(body, "openai", strictRequest("request-b"))
	if !reportA.Changed || !bytes.Contains(updatedA, []byte(`\"value\":[]`)) {
		t.Fatalf("request A = %s, report=%#v", updatedA, reportA)
	}
	if reportB.Changed || !bytes.Equal(updatedB, body) || len(reportB.Issues) != 1 {
		t.Fatalf("request B = %s, report=%#v", updatedB, reportB)
	}

	completion, _ := json.Marshal(requestCompletion{RequestID: "request-a"})
	if _, errHandle := handleRequestComplete(completion); errHandle != nil {
		t.Fatalf("handleRequestComplete() error = %v", errHandle)
	}
	if strictRequest("request-a") != nil || strictRequest("request-b") == nil {
		t.Fatal("request lifecycle cleanup removed the wrong state")
	}
}

func TestStrictResponseStateIsConcurrent(t *testing.T) {
	const requests = 32
	states := make([]*strictRequestState, requests)
	for index := range states {
		states[index] = installStrictTestState(t, fmt.Sprintf("concurrent-%d", index), map[string]map[string]any{
			"tool": schema(map[string]any{"items": map[string]any{"type": "array"}}),
		}, nil)
	}
	var wait sync.WaitGroup
	errors := make(chan error, requests)
	for index := 0; index < requests; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call","type":"function","function":{"name":"tool","arguments":"{\"items\":\"[]\"}"}}]}}]}`)
			updated, report := repairStrictResponse(body, "openai", states[index])
			if !report.Changed || !bytes.Contains(updated, []byte(`\"items\":[]`)) {
				errors <- fmt.Errorf("request %d not repaired: %s", index, updated)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
