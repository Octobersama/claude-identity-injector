package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRestoreStrictToolNamesNonStream(t *testing.T) {
	raw := []byte(`{"id":"msg-1","type":"message","content":[{"type":"tool_use","id":"tool-1","name":"Edit","input":{"patch":"x"}}]}`)
	updated, restored := restoreStrictToolNames(raw, map[string]string{"Edit": "apply_patch"})
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	var response struct {
		Content []struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if errUnmarshal := json.Unmarshal(updated, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if response.Content[0].Name != "apply_patch" || response.Content[0].Input["patch"] != "x" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRestoreStrictToolNamesSSE(t *testing.T) {
	raw := []byte("event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"Read\",\"input\":{}}}\r\n\r\n")
	updated, restored := restoreStrictToolNames(raw, map[string]string{"Read": "read_file"})
	if restored != 1 || !bytes.Contains(updated, []byte(`"name":"read_file"`)) {
		t.Fatalf("restoreStrictToolNames() = (%q, %d)", updated, restored)
	}
	if !bytes.Contains(updated, []byte("event: content_block_start\r\n")) || !bytes.HasSuffix(updated, []byte("\r\n\r\n")) {
		t.Fatalf("SSE framing changed: %q", updated)
	}
}

func TestRestoreStrictToolNamesLeavesOtherResponsesUnchanged(t *testing.T) {
	raw := []byte(`{"type":"message","content":[{"type":"text","text":"Bash"}]}`)
	updated, restored := restoreStrictToolNames(raw, map[string]string{"Bash": "run_shell"})
	if restored != 0 || !bytes.Equal(updated, raw) {
		t.Fatalf("non-tool response changed: (%s, %d)", updated, restored)
	}
}

func TestHandleResponseNormalizeBeforeRestoresMappedName(t *testing.T) {
	configState.Lock()
	previous := configState.value
	next := defaultConfig()
	next.Active = true
	configState.value = next
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})

	raw, errMarshal := json.Marshal(responseTransformRequest{
		FromFormat:        "claude",
		ToFormat:          "openai",
		Model:             "claude-test",
		Stream:            true,
		TranslatedRequest: []byte(`{"tools":[{"name":"read_file","description":"read","input_schema":{"type":"object"}}]}`),
		Body:              []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{}}}`),
	})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	envelopeRaw, errHandle := handleResponseNormalizeBefore(raw)
	if errHandle != nil {
		t.Fatalf("handleResponseNormalizeBefore() error = %v", errHandle)
	}
	var envelope struct {
		Result payloadResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(envelopeRaw, &envelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if !bytes.Contains(envelope.Result.Body, []byte(`"name":"read_file"`)) {
		t.Fatalf("normalized body = %s", envelope.Result.Body)
	}
}

func TestHandleResponseNormalizeBeforeSkipsWhenInactive(t *testing.T) {
	configState.Lock()
	previous := configState.value
	next := defaultConfig()
	next.Active = false
	configState.value = next
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})

	raw, _ := json.Marshal(responseTransformRequest{FromFormat: "claude", ToFormat: "openai", Body: []byte(`{"type":"tool_use","name":"Bash"}`)})
	envelopeRaw, errHandle := handleResponseNormalizeBefore(raw)
	if errHandle != nil {
		t.Fatalf("handleResponseNormalizeBefore() error = %v", errHandle)
	}
	var envelope struct {
		Result payloadResponse `json:"result"`
	}
	_ = json.Unmarshal(envelopeRaw, &envelope)
	if len(envelope.Result.Body) != 0 {
		t.Fatalf("inactive plugin returned body = %s", envelope.Result.Body)
	}
}

func TestNormalizeToolArgumentsUsesOpenAISchema(t *testing.T) {
	rawRequest := []byte(`{"tools":[{"type":"function","function":{"name":"AstGrepReplace","parameters":{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}},"dry_run":{"type":"boolean"},"count":{"type":"integer"},"label":{"type":"string"}}}}}]}`)
	rawResponse := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"AstGrepReplace","arguments":"{\"paths\":\"[\\\"a.py\\\"]\",\"dry_run\":\"false\",\"count\":\"3\",\"label\":\"[keep]\"}"}}]}}]}`)
	updated, fixes := normalizeToolArguments(rawResponse, toolSchemasFromRequest(rawRequest))
	if len(fixes) != 3 {
		t.Fatalf("fixes = %#v, want 3", fixes)
	}
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(updated, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	var arguments struct {
		Paths  []string `json:"paths"`
		DryRun bool     `json:"dry_run"`
		Count  int      `json:"count"`
		Label  string   `json:"label"`
	}
	if errUnmarshal := json.Unmarshal([]byte(response.Choices[0].Message.ToolCalls[0].Function.Arguments), &arguments); errUnmarshal != nil {
		t.Fatalf("arguments Unmarshal() error = %v", errUnmarshal)
	}
	if len(arguments.Paths) != 1 || arguments.Paths[0] != "a.py" || arguments.DryRun || arguments.Count != 3 || arguments.Label != "[keep]" {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestNormalizeToolArgumentsSSEPreservesFraming(t *testing.T) {
	rawRequest := []byte(`{"tools":[{"name":"TodoWrite","parameters":{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object"}}}}}]}`)
	raw := []byte("event: delta\r\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"TodoWrite\",\"arguments\":\"{\\\"todos\\\":\\\"[]\\\"}\"}}]}}]}\r\n\r\n")
	updated, fixes := normalizeToolArguments(raw, toolSchemasFromRequest(rawRequest))
	if len(fixes) != 1 || !bytes.Contains(updated, []byte(`"arguments":"{\"todos\":[]}"`)) {
		t.Fatalf("updated = %q, fixes = %#v", updated, fixes)
	}
	if !bytes.HasPrefix(updated, []byte("event: delta\r\ndata: ")) || !bytes.HasSuffix(updated, []byte("\r\n\r\n")) {
		t.Fatalf("SSE framing changed: %q", updated)
	}
}

func TestNormalizeToolArgumentsDoesNotChangeAmbiguousOrStringFields(t *testing.T) {
	rawRequest := []byte(`{"tools":[{"name":"Tool","parameters":{"type":"object","properties":{"value":{"type":["string","array"]},"text":{"type":"string"}}}}]}`)
	raw := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Tool","arguments":"{\"value\":\"[1]\",\"text\":\"[keep]\"}"}}]}}]}`)
	updated, fixes := normalizeToolArguments(raw, toolSchemasFromRequest(rawRequest))
	if len(fixes) != 0 || !bytes.Equal(updated, raw) {
		t.Fatalf("updated = %q, fixes = %#v", updated, fixes)
	}
}

func TestNormalizeToolArgumentsRejectsTrailingJSON(t *testing.T) {
	rawRequest := []byte(`{"tools":[{"name":"Tool","parameters":{"type":"object","properties":{"items":{"type":"array"}}}}]}`)
	raw := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Tool","arguments":"{\"items\":\"[] trailing\"}"}}]}}]}`)
	updated, fixes := normalizeToolArguments(raw, toolSchemasFromRequest(rawRequest))
	if len(fixes) != 0 || !bytes.Equal(updated, raw) {
		t.Fatalf("updated = %q, fixes = %#v", updated, fixes)
	}
}

func TestNormalizeToolArgumentsRejectsMultipleJSONValues(t *testing.T) {
	rawRequest := []byte(`{"tools":[{"name":"Tool","parameters":{"type":"object","properties":{"items":{"type":"array"}}}}]}`)
	raw := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Tool","arguments":"{\"items\":\"[] {}\"}"}}]}}]}`)
	updated, fixes := normalizeToolArguments(raw, toolSchemasFromRequest(rawRequest))
	if len(fixes) != 0 || !bytes.Equal(updated, raw) {
		t.Fatalf("updated = %q, fixes = %#v", updated, fixes)
	}
}

func TestHandleResponseNormalizeAfterRepairsArguments(t *testing.T) {
	configState.Lock()
	previous := configState.value
	next := defaultConfig()
	next.Active = true
	configState.value = next
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})

	raw, errMarshal := json.Marshal(responseTransformRequest{
		FromFormat:      "claude",
		ToFormat:        "openai",
		Model:           "claude-opus-5",
		OriginalRequest: []byte(`{"tools":[{"type":"function","function":{"name":"TodoWrite","parameters":{"type":"object","properties":{"todos":{"type":"array"}}}}}]}`),
		Body:            []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"TodoWrite","arguments":"{\"todos\":\"[]\"}"}}]}}]}`),
	})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	envelopeRaw, errHandle := handleResponseNormalizeAfter(raw)
	if errHandle != nil {
		t.Fatalf("handleResponseNormalizeAfter() error = %v", errHandle)
	}
	var envelope struct {
		Result payloadResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(envelopeRaw, &envelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if !bytes.Contains(envelope.Result.Body, []byte(`"arguments":"{\"todos\":[]}"`)) {
		t.Fatalf("normalized body = %s", envelope.Result.Body)
	}
}
