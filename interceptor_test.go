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
	if len(body.Tools) != 6 {
		t.Fatalf("mapped tool count = %d, want 6", len(body.Tools))
	}
	for index, name := range strictCoreToolNames {
		tool := body.Tools[index]
		if tool.Name != name || tool.Description != "client tool" || tool.InputSchema["type"] != "object" {
			t.Fatalf("tools[%d] = %#v", index, tool)
		}
	}
	if toolMapping.Strategy != "client_tools" || toolMapping.ClientToolCount != 1 || toolMapping.UpstreamToolCount != 6 {
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
	raw, _ := json.Marshal(upstreamRequest{Phase: "final", ToFormat: "claude", Auth: upstreamAuth{Index: "idx-1"}, Body: []byte(`{"model":"claude-test","messages":[]}`)})
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
