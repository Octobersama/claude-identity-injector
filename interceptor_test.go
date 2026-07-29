package main

import (
	"encoding/json"
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

func TestInjectIdentityTreatsAnyBlockAsIdempotent(t *testing.T) {
	first := []byte(`{"system":[{"type":"text","text":"` + identityPrompt + `"},{"type":"text","text":"Keep this"}]}`)
	updated, outcome, errInject := injectIdentity(first, "prepend")
	if errInject != nil || outcome != "already_present" || updated != nil {
		t.Fatalf("first identity result = (%s, %q, %v), want nil/already_present/nil", updated, outcome, errInject)
	}

	later := []byte(`{"system":[{"type":"text","text":"Keep this"},{"type":"text","text":"` + identityPrompt + `"}]}`)
	updated, outcome, errInject = injectIdentity(later, "prepend")
	if errInject != nil || outcome != "already_present" || updated != nil {
		t.Fatalf("later identity result = (%s, %q, %v), want nil/already_present/nil", updated, outcome, errInject)
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
