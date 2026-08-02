package main

import (
	"strings"
	"testing"
)

func TestSummarizeStrictBodyOmitsValues(t *testing.T) {
	summary := summarizeStrictBody([]byte(`{
		"model":"claude-opus-5",
		"system":[{"type":"text","text":"sensitive system"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"sensitive message"},{"type":"tool_result","tool_use_id":"secret"}]}],
		"tools":[{"name":"Bash","description":"secret","input_schema":{"type":"object"}}],
		"metadata":{"client":"keep","user_id":"{\"device_id\":\"secret\",\"session_id\":\"secret\"}"},
		"thinking":{"type":"enabled"},
		"context_management":{},
		"output_config":{}
	}`))

	if summary.TopLevelKeys != "context_management,messages,metadata,model,output_config,system,thinking,tools" {
		t.Fatalf("top-level keys = %q", summary.TopLevelKeys)
	}
	if summary.SystemBlocks != 1 || summary.SystemTypes != "text" {
		t.Fatalf("system summary = %#v", summary)
	}
	if summary.MessageCount != 1 || summary.MessageRoles != "user" || summary.MessageContentTypes != "text,tool_result" {
		t.Fatalf("message summary = %#v", summary)
	}
	if summary.ToolCount != 1 || summary.ToolNames != "Bash" {
		t.Fatalf("tool summary = %#v", summary)
	}
	if summary.MetadataKeys != "client,user_id" || summary.MetadataUserIDKeys != "device_id,session_id" {
		t.Fatalf("metadata summary = %#v", summary)
	}
	if !summary.HasThinking || !summary.HasContext || !summary.HasOutputConfig {
		t.Fatalf("optional fields = %#v", summary)
	}
}

func TestSummarizeStrictBodyHandlesStringContentAndInvalidJSON(t *testing.T) {
	summary := summarizeStrictBody([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if summary.MessageCount != 1 || summary.MessageContentTypes != "string" {
		t.Fatalf("string content summary = %#v", summary)
	}
	if invalid := summarizeStrictBody([]byte(`not json`)); invalid != (strictBodySummary{}) {
		t.Fatalf("invalid summary = %#v", invalid)
	}
}

func TestStrictBodySummaryCompactOmitsPayloadValues(t *testing.T) {
	compact := summarizeStrictBody([]byte(`{"system":[{"type":"text","text":"secret system"}],"messages":[{"role":"user","content":"secret message"}],"tools":[{"name":"Bash","description":"secret description"}]}`)).compact()
	if strings.Contains(compact, "secret") {
		t.Fatalf("compact summary contains payload data: %q", compact)
	}
	if !strings.Contains(compact, "tool_names=Bash") || !strings.Contains(compact, "message_roles=user") {
		t.Fatalf("compact summary = %q", compact)
	}
}
