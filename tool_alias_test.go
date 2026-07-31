package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildStrictClientToolMappingUsesCanonicalNames(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},
		{"name":"apply_patch","description":"edit","input_schema":{"type":"object"}},
		{"name":"glob","description":"glob","input_schema":{"type":"object"}},
		{"name":"search","description":"grep","input_schema":{"type":"object"}},
		{"name":"read_file","description":"read","input_schema":{"type":"object"}},
		{"name":"write_file","description":"write","input_schema":{"type":"object"}}
	]`)

	updated, mapping, errMapping := buildStrictClientToolMapping(raw)
	if errMapping != nil {
		t.Fatalf("buildStrictClientToolMapping() error = %v", errMapping)
	}
	var tools []map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(updated, &tools); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	var names []string
	for _, tool := range tools {
		var name string
		_ = json.Unmarshal(tool["name"], &name)
		names = append(names, name)
	}
	if !reflect.DeepEqual(names, strictCoreToolNames) {
		t.Fatalf("tool names = %v, want %v", names, strictCoreToolNames)
	}
	if string(tools[0]["description"]) != `"run"` || string(tools[0]["input_schema"]) != `{"type":"object","properties":{"command":{"type":"string"}}}` {
		t.Fatalf("client tool fields changed: %s", updated)
	}
	if mapping.AliasCount != 6 || mapping.FallbackCount != 0 {
		t.Fatalf("mapping counts = %#v", mapping)
	}
}

func TestApplyStrictClientToolMappingRewritesToolReferences(t *testing.T) {
	body := map[string]json.RawMessage{
		"tools":       json.RawMessage(`[{"name":"run_shell","description":"run","input_schema":{"type":"object"}}]`),
		"tool_choice": json.RawMessage(`{"type":"tool","name":"run_shell"}`),
		"messages":    json.RawMessage(`[{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"run_shell","input":{}},{"type":"tool_reference","tool_name":"run_shell"}]}]`),
	}

	mapping, errMapping := applyStrictClientToolMapping(body, "openai")
	if errMapping != nil {
		t.Fatalf("applyStrictClientToolMapping() error = %v", errMapping)
	}
	if mapping.ClientToCanonical["run_shell"] != "Bash" {
		t.Fatalf("client mapping = %#v", mapping.ClientToCanonical)
	}
	var choice struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body["tool_choice"], &choice)
	if choice.Name != "Bash" {
		t.Fatalf("tool_choice.name = %q", choice.Name)
	}
	var messages []struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(body["messages"], &messages)
	if messages[0].Content[0]["name"] != "Bash" || messages[0].Content[1]["tool_name"] != "Bash" {
		t.Fatalf("tool references = %#v", messages[0].Content)
	}
}

func TestApplyStrictClientToolMappingPreservesNativeClaudeTools(t *testing.T) {
	body := map[string]json.RawMessage{
		"tools": json.RawMessage(`[{"name":"CustomTool","description":"native","input_schema":{"type":"object"}}]`),
	}

	mapping, errMapping := applyStrictClientToolMapping(body, "claude")
	if errMapping != nil {
		t.Fatalf("applyStrictClientToolMapping() error = %v", errMapping)
	}
	if mapping.Strategy != "preserved_native" || string(body["tools"]) != `[{"name":"CustomTool","description":"native","input_schema":{"type":"object"}}]` {
		t.Fatalf("native tools changed: mapping=%#v tools=%s", mapping, body["tools"])
	}
}
