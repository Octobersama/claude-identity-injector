package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestInjectIdentitySystemShapes(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantTexts []string
	}{
		{name: "missing", body: `{"messages":[]}`, wantTexts: []string{identityPrompt}},
		{name: "null", body: `{"system":null}`, wantTexts: []string{identityPrompt}},
		{name: "empty string", body: `{"system":""}`, wantTexts: []string{identityPrompt}},
		{name: "string", body: `{"system":"Keep this"}`, wantTexts: []string{identityPrompt, "Keep this"}},
		{name: "empty array", body: `{"system":[]}`, wantTexts: []string{identityPrompt}},
		{name: "array", body: `{"system":[{"type":"text","text":"Keep this"}]}`, wantTexts: []string{identityPrompt, "Keep this"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, outcome, errInject := injectIdentity([]byte(tt.body), "compatible")
			if errInject != nil {
				t.Fatalf("injectIdentity() error = %v", errInject)
			}
			if outcome != "injected" {
				t.Fatalf("outcome = %q, want injected", outcome)
			}
			var body struct {
				System []systemBlock `json:"system"`
			}
			if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
				t.Fatalf("Unmarshal() error = %v", errUnmarshal)
			}
			if len(body.System) != len(tt.wantTexts) {
				t.Fatalf("system length = %d, want %d: %s", len(body.System), len(tt.wantTexts), updated)
			}
			for index, want := range tt.wantTexts {
				if got := body.System[index].Text; got != want {
					t.Fatalf("system[%d].text = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func TestStrictProfileMatchesCapturedClaudeCodeShape(t *testing.T) {
	req := upstreamRequest{
		RequestID: "request-1",
		Stream:    true,
		Auth:      upstreamAuth{ID: "auth-1", Index: "idx-1"},
		Body:      []byte(`{"model":"claude-opus-5","messages":[],"tools":[]}`),
	}
	updated, headers, clearHeaders, toolMapping, errStrict := applyStrictClaudeCodeProfile(req)
	if errStrict != nil {
		t.Fatalf("applyStrictClaudeCodeProfile() error = %v", errStrict)
	}
	var body struct {
		System []strictTextBlock `json:"system"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if len(body.System) != 3 || !strings.HasPrefix(body.System[0].Text, "x-anthropic-billing-header: cc_version=2.1.220.685") {
		t.Fatalf("system = %#v", body.System)
	}
	if body.System[1].Text != identityPrompt || body.System[1].CacheControl == nil || body.System[1].CacheControl.Type != "ephemeral" {
		t.Fatalf("identity block = %#v", body.System[1])
	}
	if body.System[2].Text != strictHarnessPrompt || body.System[2].CacheControl == nil || body.System[2].CacheControl.Type != "ephemeral" {
		t.Fatalf("harness block length = %d, cache = %#v", len(body.System[2].Text), body.System[2].CacheControl)
	}
	var toolNames []string
	for _, tool := range body.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	for _, want := range []string{"Bash", "Edit", "Glob", "Grep", "Read", "Write"} {
		if !slices.Contains(toolNames, want) {
			t.Fatalf("injected tools missing %s: %v", want, toolNames)
		}
	}
	if !strings.Contains(body.Metadata.UserID, `"device_id"`) || !strings.Contains(body.Metadata.UserID, `"session_id"`) {
		t.Fatalf("metadata.user_id = %q", body.Metadata.UserID)
	}
	if got := headers.Get("User-Agent"); got != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := headers.Get("X-Stainless-Package-Version"); got != "0.94.0" {
		t.Fatalf("X-Stainless-Package-Version = %q", got)
	}
	if got := headers["anthropic-beta"][0]; got != strictBetas {
		t.Fatalf("Anthropic-Beta = %q, want captured sample", got)
	}
	if got := headers.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := headers.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if len(clearHeaders) != 1 || clearHeaders[0] != "x-client-request-id" {
		t.Fatalf("clearHeaders = %#v", clearHeaders)
	}
	if toolMapping.Strategy != "injected_core" || toolMapping.UpstreamToolCount != 6 {
		t.Fatalf("tool mapping = %#v", toolMapping)
	}
}

func TestStrictProfileAliasesClientTools(t *testing.T) {
	req := upstreamRequest{
		RequestID: "request-tools",
		Stream:    true,
		Auth:      upstreamAuth{ID: "auth-1", Index: "idx-1"},
		Body:      []byte(`{"model":"claude-opus-5","messages":[],"tools":[{"name":"CustomTool","description":"client tool","input_schema":{"type":"object"}}]}`),
	}
	updated, _, _, toolMapping, errStrict := applyStrictClaudeCodeProfile(req)
	if errStrict != nil {
		t.Fatalf("applyStrictClaudeCodeProfile() error = %v", errStrict)
	}
	var body struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "CustomTool" || body.Tools[0].Description != "client tool" || body.Tools[0].InputSchema["type"] != "object" {
		t.Fatalf("tools = %#v", body.Tools)
	}
	if toolMapping.Strategy != "client_tools" || toolMapping.ClientToolCount != 1 || toolMapping.UpstreamToolCount != 1 || toolMapping.FallbackCount != 0 {
		t.Fatalf("tool mapping = %#v", toolMapping)
	}
}

func TestStrictProfileInjectsToolsWhenMissing(t *testing.T) {
	req := upstreamRequest{
		RequestID: "request-no-tools",
		Stream:    true,
		Auth:      upstreamAuth{ID: "auth-1", Index: "idx-1"},
		Body:      []byte(`{"model":"claude-opus-5","messages":[]}`),
	}
	updated, _, _, toolMapping, errStrict := applyStrictClaudeCodeProfile(req)
	if errStrict != nil {
		t.Fatalf("applyStrictClaudeCodeProfile() error = %v", errStrict)
	}
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if len(body.Tools) != 6 {
		t.Fatalf("injected tool count = %d, want 6", len(body.Tools))
	}
	if toolMapping.Strategy != "injected_core" {
		t.Fatalf("tool mapping strategy = %q", toolMapping.Strategy)
	}
}

func TestStrictCoreProfileInjectsToolsOnlyWhenMissing(t *testing.T) {
	missing := upstreamRequest{Body: []byte(`{"model":"claude-opus-5","messages":[]}`)}
	updated, _, _, mapping, controls, errStrict := applyStrictClaudeCodeProfileWithProfile(missing, "minimal_core")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	if !controls.InjectCoreTools || controls.MapTools || mapping.Strategy != "injected_core" {
		t.Fatalf("controls/mapping = %#v %#v", controls, mapping)
	}
	var injected struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(updated, &injected); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(injected.Tools) != len(strictCoreToolNames) {
		t.Fatalf("tool count = %d", len(injected.Tools))
	}

	existing := upstreamRequest{Body: []byte(`{"model":"claude-opus-5","messages":[],"tools":[{"name":"client_tool","input_schema":{"type":"object"}}]}`)}
	preserved, _, _, mapping, _, errStrict := applyStrictClaudeCodeProfileWithProfile(existing, "minimal_core")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	if !slices.Equal(preserved, existing.Body) || mapping.Strategy != "preserved_native" {
		t.Fatalf("existing tools changed: body=%s mapping=%#v", preserved, mapping)
	}
}

func TestStrictProfileAblationPresets(t *testing.T) {
	req := upstreamRequest{
		RequestID:    "request-ablation",
		SourceFormat: "openai",
		Auth:         upstreamAuth{ID: "auth-a", Index: "index-a"},
		Body:         []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"client system"}],"messages":[],"tools":[{"name":"run_shell","description":"client tool","input_schema":{"type":"object"}}]}`),
	}
	for _, profile := range []string{"minimum", "minimal", "bearer", "bearer_http1", "minimal_core", "identity", "system", "body", "body_core", "headers", "headers_soft", "body_headers", "body_headers_core", "full"} {
		t.Run(profile, func(t *testing.T) {
			updated, headers, clearHeaders, mapping, controls, errStrict := applyStrictClaudeCodeProfileWithProfile(req, profile)
			if errStrict != nil {
				t.Fatal(errStrict)
			}
			wantBetas := strictBetas
			if profile == "minimum" {
				wantBetas = strictMinimumBetas
			}
			if values := headers["anthropic-beta"]; len(values) != 1 || values[0] != wantBetas {
				t.Fatalf("anthropic-beta = %#v", values)
			}
			fullHeaders := profile == "headers" || profile == "body_headers" || profile == "body_headers_core" || profile == "full"
			softHeaders := profile == "headers_soft"
			wantBearer := profile == "bearer" || profile == "bearer_http1" || fullHeaders
			wantHTTP1 := profile == "bearer_http1" || fullHeaders
			wantReplaceHeaders := profile == "minimum" || fullHeaders || softHeaders
			if controls.ReplaceHeaders != wantReplaceHeaders || controls.ForceHTTP1 != wantHTTP1 || controls.ForceBearerAuthorization != wantBearer || controls.ForceAPIKeyAuthentication {
				t.Fatalf("header controls = %#v", controls)
			}
			if fullHeaders || softHeaders {
				if len(clearHeaders) != 1 || clearHeaders[0] != "x-client-request-id" {
					t.Fatalf("clear headers = %v", clearHeaders)
				}
			} else if len(clearHeaders) != 0 {
				t.Fatalf("clear headers = %v, want none", clearHeaders)
			}
			if (profile == "minimal" || profile == "bearer" || profile == "bearer_http1" || profile == "headers" || profile == "headers_soft") && !slices.Equal(updated, req.Body) {
				t.Fatalf("profile %q changed body bytes:\n got: %s\nwant: %s", profile, updated, req.Body)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(updated, &body); err != nil {
				t.Fatal(err)
			}
			var system []strictTextBlock
			if err := json.Unmarshal(body["system"], &system); err != nil {
				t.Fatal(err)
			}
			switch profile {
			case "minimal", "bearer", "bearer_http1", "minimal_core", "headers", "headers_soft":
				if len(system) != 1 || system[0].Text != "client system" {
					t.Fatalf("system = %#v", system)
				}
			case "minimum":
				if len(system) != 1 || system[0].Text != identityPrompt {
					t.Fatalf("system = %#v", system)
				}
				if !controls.ClientSystemRelocated || controls.ClientSystemBlocksMoved != 1 || controls.ClientSystemBytesMoved != len("client system") {
					t.Fatalf("minimum controls = %#v", controls)
				}
			case "identity":
				if len(system) != 2 || system[0].Text != identityPrompt || system[1].Text != "client system" {
					t.Fatalf("system = %#v", system)
				}
			default:
				if len(system) != 3 || system[1].Text != identityPrompt || system[2].Text != strictHarnessPrompt {
					t.Fatalf("system = %#v", system)
				}
			}
			_, hasMetadata := body["metadata"]
			wantMetadata := profile == "minimum" || profile == "body" || profile == "body_core" || profile == "body_headers" || profile == "body_headers_core" || profile == "full"
			if hasMetadata != wantMetadata {
				t.Fatalf("metadata present = %t, want %t", hasMetadata, wantMetadata)
			}
			if profile == "full" || profile == "minimum" {
				wantStrategy := "client_tools"
				if profile == "minimum" {
					wantStrategy = "client_tools_padded"
				}
				if mapping.AliasCount != 1 || mapping.Strategy != wantStrategy {
					t.Fatalf("mapping = %#v", mapping)
				}
			} else if mapping.AliasCount != 0 || mapping.Strategy != "preserved_native" {
				t.Fatalf("mapping = %#v", mapping)
			}
		})
	}
}

func TestMinimumStrictProfilePreservesClientBodyAndAddsOnlyFingerprint(t *testing.T) {
	req := upstreamRequest{
		RequestID:    "minimum-request",
		SourceFormat: "openai",
		Auth:         upstreamAuth{ID: "auth-a", Index: "index-a"},
		Body: []byte(`{
			"model":"claude-opus-5",
			"system":[{"type":"text","text":"client system"}],
			"messages":[],
			"metadata":{"client":"keep"},
			"thinking":{"type":"enabled","budget_tokens":1024},
			"context_management":{"client":true},
			"output_config":{"effort":"low"},
			"tools":[{"name":"run_shell","description":"run","input_schema":{"type":"object"}},{"name":"CustomTool","description":"custom","input_schema":{"type":"object"}}]
		}`),
	}
	updated, headers, clearHeaders, mapping, controls, errStrict := applyStrictClaudeCodeProfileWithProfile(req, "minimum")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	if !controls.MinimumFingerprint || controls.FullSystem || controls.FullBody || controls.FullHeaders || controls.ForceHTTP1 || controls.ForceBearerAuthorization || controls.ForceAPIKeyAuthentication || !controls.ReplaceHeaders {
		t.Fatalf("controls = %#v", controls)
	}
	if len(clearHeaders) != 0 || len(headers) != 4 || len(headers["anthropic-beta"]) != 1 || headers["anthropic-beta"][0] != strictMinimumBetas || headers.Get("Anthropic-Version") != "2023-06-01" || headers.Get("Content-Type") != "application/json" || headers.Get("User-Agent") != "claude-cli/2.1.220 (external, cli)" {
		t.Fatalf("headers=%#v clear=%v", headers, clearHeaders)
	}
	var body struct {
		System   []strictTextBlock `json:"system"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []strictTextBlock `json:"content"`
		} `json:"messages"`
		Metadata          map[string]json.RawMessage `json:"metadata"`
		Thinking          json.RawMessage            `json:"thinking"`
		ContextManagement json.RawMessage            `json:"context_management"`
		OutputConfig      json.RawMessage            `json:"output_config"`
		Tools             []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(body.System) != 1 || body.System[0].Text != identityPrompt {
		t.Fatalf("system = %#v", body.System)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "<system-reminder>\nclient system\n</system-reminder>" {
		t.Fatalf("messages = %#v", body.Messages)
	}
	if !controls.ClientSystemRelocated || controls.ClientSystemBlocksMoved != 1 || controls.ClientSystemBytesMoved != len("client system") {
		t.Fatalf("controls = %#v", controls)
	}
	if string(body.Metadata["client"]) != `"keep"` {
		t.Fatalf("metadata client field = %s", body.Metadata["client"])
	}
	var userID string
	if errUnmarshal := json.Unmarshal(body.Metadata["user_id"], &userID); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !strings.Contains(userID, `"device_id"`) || !strings.Contains(userID, `"session_id"`) || strings.Contains(userID, `"account_uuid"`) {
		t.Fatalf("metadata.user_id = %q", userID)
	}
	if !strings.Contains(string(body.Thinking), `"budget_tokens":1024`) || !strings.Contains(string(body.ContextManagement), `"client":true`) || !strings.Contains(string(body.OutputConfig), `"effort":"low"`) {
		t.Fatalf("client body fields changed: thinking=%s context=%s output=%s", body.Thinking, body.ContextManagement, body.OutputConfig)
	}
	toolNames := make([]string, 0, len(body.Tools))
	for _, tool := range body.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	if !slices.Contains(toolNames, "Bash") || !slices.Contains(toolNames, "CustomTool") || mapping.CoreToolCount != 3 || mapping.Strategy != "client_tools_padded" {
		t.Fatalf("tools=%v mapping=%#v", toolNames, mapping)
	}
}

func TestMinimumStrictProfileKeepsNativeClaudeSystemAtTopLevel(t *testing.T) {
	req := upstreamRequest{
		SourceFormat: "claude",
		Auth:         upstreamAuth{ID: "auth-a", Index: "index-a"},
		Body:         []byte(`{"system":[{"type":"text","text":"client system"}],"messages":[{"role":"user","content":"hello"}]}`),
	}
	updated, _, _, _, controls, errStrict := applyStrictClaudeCodeProfileWithProfile(req, "minimum")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	var body struct {
		System []strictTextBlock `json:"system"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(body.System) != 2 || body.System[0].Text != identityPrompt || body.System[1].Text != "client system" {
		t.Fatalf("system = %#v", body.System)
	}
	if controls.ClientSystemRelocated || controls.ClientSystemBlocksMoved != 0 || controls.ClientSystemBytesMoved != 0 {
		t.Fatalf("controls = %#v", controls)
	}
}

func TestMinimumStrictProfilePrependsClientSystemReminderToExistingUserContent(t *testing.T) {
	req := upstreamRequest{
		SourceFormat: "openai",
		Auth:         upstreamAuth{ID: "auth-a", Index: "index-a"},
		Body: []byte(`{
			"system":[{"type":"text","text":"rule one"},{"type":"text","text":"rule two"}],
			"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
		}`),
	}
	updated, _, _, _, controls, errStrict := applyStrictClaudeCodeProfileWithProfile(req, "minimum")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	var body struct {
		System   []strictTextBlock `json:"system"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []strictTextBlock `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(body.System) != 1 || body.System[0].Text != identityPrompt {
		t.Fatalf("system = %#v", body.System)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 || body.Messages[0].Content[0].Text != "<system-reminder>\nrule one\n\nrule two\n</system-reminder>" || body.Messages[0].Content[1].Text != "hello" {
		t.Fatalf("messages = %#v", body.Messages)
	}
	if !controls.ClientSystemRelocated || controls.ClientSystemBlocksMoved != 2 || controls.ClientSystemBytesMoved != len("rule onerule two") {
		t.Fatalf("controls = %#v", controls)
	}
}

func TestMinimumStrictProfileInjectsOnlyReadOnlyMarkersWhenToolsMissing(t *testing.T) {
	req := upstreamRequest{
		RequestID: "minimum-missing-tools",
		Auth:      upstreamAuth{ID: "auth-a", Index: "index-a"},
		Body:      []byte(`{"model":"claude-opus-5","messages":[]}`),
	}
	updated, _, _, mapping, _, errStrict := applyStrictClaudeCodeProfileWithProfile(req, "minimum")
	if errStrict != nil {
		t.Fatal(errStrict)
	}
	var body struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(body.Tools) != 3 || mapping.Strategy != "injected_minimum" || mapping.CoreToolCount != 3 {
		t.Fatalf("tools=%#v mapping=%#v", body.Tools, mapping)
	}
	for _, tool := range body.Tools {
		if !strings.Contains(tool.Description, "unavailable for execution") {
			t.Fatalf("tool %s description = %q", tool.Name, tool.Description)
		}
	}
}

func TestStrictRuleRequestsCloakBypassOnlyInPrePhase(t *testing.T) {
	cfg, errParse := parseConfig([]byte(`
active: true
rules:
  - id: strict
    enabled: true
    strict_mode: true
    match_auths: true
    auth_indexes: [idx-1]
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	configState.Lock()
	previous := configState.value
	configState.value = cfg
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})
	raw, _ := json.Marshal(upstreamRequest{Phase: "pre_cloak", ToFormat: "claude", Auth: upstreamAuth{Index: "idx-1"}, Body: []byte(`{"messages":[]}`)})
	envelopeRaw, errHandle := handleUpstreamIntercept(raw)
	if errHandle != nil {
		t.Fatalf("handleUpstreamIntercept() error = %v", errHandle)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(envelopeRaw, &envelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal envelope: %v", errUnmarshal)
	}
	var response upstreamResponse
	if errUnmarshal := json.Unmarshal(envelope.Result, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal response: %v", errUnmarshal)
	}
	if !response.BypassClaudeCloak || len(response.Body) != 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestStrictRuleRequestsHTTP1InFinalPhase(t *testing.T) {
	const requestID = "strict-final"
	deleteStrictRequest(requestID)
	t.Cleanup(func() { deleteStrictRequest(requestID) })
	cfg, errParse := parseConfig([]byte(`
active: true
rules:
  - id: strict
    enabled: true
    strict_mode: true
    match_auths: true
    auth_indexes: [idx-1]
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	configState.Lock()
	previous := configState.value
	configState.value = cfg
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})
	raw, _ := json.Marshal(upstreamRequest{RequestID: requestID, Phase: "final", ToFormat: "claude", Auth: upstreamAuth{Index: "idx-1"}, Body: []byte(`{"model":"claude-test","messages":[]}`)})
	envelopeRaw, errHandle := handleUpstreamIntercept(raw)
	if errHandle != nil {
		t.Fatalf("handleUpstreamIntercept() error = %v", errHandle)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(envelopeRaw, &envelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal envelope: %v", errUnmarshal)
	}
	var response upstreamResponse
	if errUnmarshal := json.Unmarshal(envelope.Result, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal response: %v", errUnmarshal)
	}
	if !response.ForceHTTP1 || !response.ReplaceHeaders || !response.ForceBearerAuthorization || !response.SkipUpstreamBodyTransforms {
		t.Fatalf("strict response = %#v", response)
	}
	if strictRequest(requestID) == nil {
		t.Fatal("strict response state was not stored")
	}
}

func TestFinalRetryClearsPriorStrictResponseStateWhenRuleDoesNotMatch(t *testing.T) {
	const requestID = "strict-retry"
	installStrictTestState(t, requestID, map[string]map[string]any{}, nil)
	cfg, errParse := parseConfig([]byte(`
active: true
rules:
  - id: strict
    enabled: true
    strict_mode: true
    match_auths: true
    auth_indexes: [idx-1]
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	configState.Lock()
	previous := configState.value
	configState.value = cfg
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})

	raw, _ := json.Marshal(upstreamRequest{RequestID: requestID, Phase: "final", ToFormat: "claude", Auth: upstreamAuth{Index: "idx-2"}, Body: []byte(`{"messages":[]}`)})
	if _, errHandle := handleUpstreamIntercept(raw); errHandle != nil {
		t.Fatalf("handleUpstreamIntercept() error = %v", errHandle)
	}
	if strictRequest(requestID) != nil {
		t.Fatal("unmatched retry retained prior strict response state")
	}
}

func TestInjectIdentityOnlyTreatsFirstBlockAsIdempotentWithoutCloak(t *testing.T) {
	first := []byte(`{"system":[{"type":"text","text":"` + identityPrompt + `"},{"type":"text","text":"Keep this"}]}`)
	updated, outcome, errInject := injectIdentity(first, "prepend")
	if errInject != nil || outcome != "already_present" || updated != nil {
		t.Fatalf("first identity result = (%s, %q, %v), want nil/already_present/nil", updated, outcome, errInject)
	}

	later := []byte(`{"system":[{"type":"text","text":"Keep this"},{"type":"text","text":"` + identityPrompt + `"}]}`)
	updated, outcome, errInject = injectIdentity(later, "prepend")
	if errInject != nil || outcome != "injected" || len(updated) == 0 {
		t.Fatalf("later identity result = (%s, %q, %v), want injected body", updated, outcome, errInject)
	}
}

func TestInjectIdentityCloakBillingHeader(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63"}]}`)
	updated, outcome, errInject := injectIdentity(body, "skip")
	if errInject != nil || outcome != "cloak_skipped" || updated != nil {
		t.Fatalf("skip result = (%s, %q, %v), want nil/cloak_skipped/nil", updated, outcome, errInject)
	}
	updated, outcome, errInject = injectIdentity(body, "compatible")
	if errInject != nil || outcome != "injected" || len(updated) == 0 {
		t.Fatalf("compatible result = (%s, %q, %v), want injected body", updated, outcome, errInject)
	}
	var decoded struct {
		System []systemBlock `json:"system"`
	}
	if errUnmarshal := json.Unmarshal(updated, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if len(decoded.System) != 2 || decoded.System[0].Text != "x-anthropic-billing-header: cc_version=2.1.63" || decoded.System[1].Text != identityPrompt {
		t.Fatalf("compatible system = %#v", decoded.System)
	}
}

func TestInjectIdentityFailsOpenForInvalidBodies(t *testing.T) {
	for _, body := range []string{`not-json`, `[]`, `{"system":{}}`} {
		if updated, _, errInject := injectIdentity([]byte(body), "compatible"); errInject == nil || updated != nil {
			t.Fatalf("injectIdentity(%q) = (%s, %v), want nil error", body, updated, errInject)
		}
	}
}

func TestRuleMatchingAndFirstMatch(t *testing.T) {
	cfg, errParse := parseConfig([]byte(`
active: true
rules:
  - id: disabled
    enabled: false
  - id: selected
    enabled: true
    providers: [Claude]
    auth_ids: [auth-1]
    auth_indexes: [idx-1]
    requested_models: ["team/*"]
    upstream_models: ["claude-?-sonnet-*"]
  - id: later
    enabled: true
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	req := upstreamRequest{
		Provider:       "claude",
		RequestedModel: "team/sonnet",
		Model:          "claude-4-sonnet-20250514",
		Auth:           upstreamAuth{ID: "auth-1", Index: "idx-1"},
	}
	matched := firstMatchingRule(cfg.Rules, req)
	if matched == nil || matched.ID != "selected" {
		t.Fatalf("firstMatchingRule() = %#v, want selected", matched)
	}
	req.Auth.ID = "other"
	matched = firstMatchingRule(cfg.Rules, req)
	if matched == nil || matched.ID != "later" {
		t.Fatalf("firstMatchingRule() after mismatch = %#v, want later", matched)
	}
}

func TestRuleMatchingHonorsConditionToggles(t *testing.T) {
	cfg, errParse := parseConfig([]byte(`
rules:
  - id: selected
    enabled: true
    match_providers: false
    providers: [other]
    match_auths: true
    auth_indexes: [idx-1]
    match_requested_models: false
    requested_models: [other-model]
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	req := upstreamRequest{Provider: "claude", RequestedModel: "claude-sonnet-4", Auth: upstreamAuth{Index: "idx-1"}}
	if matched := firstMatchingRule(cfg.Rules, req); matched == nil || matched.ID != "selected" {
		t.Fatalf("firstMatchingRule() = %#v, want selected", matched)
	}
	req.Auth.Index = "idx-2"
	if matched := firstMatchingRule(cfg.Rules, req); matched != nil {
		t.Fatalf("firstMatchingRule() = %#v, want nil", matched)
	}
}

func TestRuleMatchingConfiguredProviderKeyByAuthIndex(t *testing.T) {
	cfg, errParse := parseConfig([]byte(`
rules:
  - id: key-entry
    enabled: true
    match_providers: true
    provider_auth_indexes: [configured-key-1]
`))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	req := upstreamRequest{Provider: "claude", Auth: upstreamAuth{Index: "configured-key-1"}}
	if matched := firstMatchingRule(cfg.Rules, req); matched == nil || matched.ID != "key-entry" {
		t.Fatalf("firstMatchingRule() = %#v, want key-entry", matched)
	}
	req.Auth.Index = "configured-key-2"
	if matched := firstMatchingRule(cfg.Rules, req); matched != nil {
		t.Fatalf("firstMatchingRule() = %#v, want nil", matched)
	}
}
