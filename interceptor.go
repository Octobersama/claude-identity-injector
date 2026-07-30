package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
)

const identityPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

type upstreamRequest struct {
	RequestID      string       `json:"RequestID"`
	Phase          string       `json:"Phase"`
	SourceFormat   string       `json:"SourceFormat"`
	ToFormat       string       `json:"ToFormat"`
	Provider       string       `json:"Provider"`
	Model          string       `json:"Model"`
	RequestedModel string       `json:"RequestedModel"`
	Stream         bool         `json:"Stream"`
	Auth           upstreamAuth `json:"Auth"`
	Headers        http.Header  `json:"Headers"`
	Body           []byte       `json:"Body"`
	HostCallbackID string       `json:"host_callback_id"`
}

type upstreamAuth struct {
	ID       string `json:"id"`
	Index    string `json:"index"`
	Name     string `json:"name"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
}

type upstreamResponse struct {
	Body              []byte      `json:"Body,omitempty"`
	Headers           http.Header `json:"Headers,omitempty"`
	ClearHeaders      []string    `json:"ClearHeaders,omitempty"`
	BypassClaudeCloak bool        `json:"BypassClaudeCloak,omitempty"`
}

type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var counters struct {
	seen         atomic.Uint64
	matched      atomic.Uint64
	injected     atomic.Uint64
	already      atomic.Uint64
	strict       atomic.Uint64
	cloakSkipped atomic.Uint64
	errors       atomic.Uint64
}

func handleUpstreamIntercept(raw []byte) ([]byte, error) {
	var req upstreamRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := currentConfig()
	if !cfg.Active || !strings.EqualFold(req.ToFormat, "claude") {
		return okEnvelope(upstreamResponse{})
	}
	counters.seen.Add(1)
	matchedRule := firstMatchingRule(cfg.Rules, req)
	if req.Phase == "pre_cloak" {
		return okEnvelope(upstreamResponse{BypassClaudeCloak: matchedRule != nil && matchedRule.StrictMode})
	}
	if matchedRule == nil {
		logHost(req.HostCallbackID, "debug", "Claude identity injection rule did not match", logFields(req, ""))
		return okEnvelope(upstreamResponse{})
	}
	counters.matched.Add(1)
	if matchedRule.StrictMode {
		updated, headers, clearHeaders, errStrict := applyStrictClaudeCodeProfile(req)
		if errStrict != nil {
			counters.errors.Add(1)
			fields := logFields(req, matchedRule.ID)
			fields["error"] = errStrict.Error()
			logHost(req.HostCallbackID, "error", "Claude strict profile failed open", fields)
			return okEnvelope(upstreamResponse{})
		}
		counters.injected.Add(1)
		counters.strict.Add(1)
		fields := logFields(req, matchedRule.ID)
		fields["outcome"] = "strict_profile"
		logHost(req.HostCallbackID, "info", "Claude strict profile took over request", fields)
		return okEnvelope(upstreamResponse{Body: updated, Headers: headers, ClearHeaders: clearHeaders})
	}

	updated, outcome, errInject := injectIdentity(req.Body, cfg.CloakHandling)
	if errInject != nil {
		counters.errors.Add(1)
		fields := logFields(req, matchedRule.ID)
		fields["error"] = errInject.Error()
		logHost(req.HostCallbackID, "error", "Claude identity injection failed open", fields)
		return okEnvelope(upstreamResponse{})
	}
	fields := logFields(req, matchedRule.ID)
	fields["outcome"] = outcome
	switch outcome {
	case "injected":
		counters.injected.Add(1)
		if cfg.LogMatches != nil && *cfg.LogMatches {
			logHost(req.HostCallbackID, "info", "Claude identity system prompt injected", fields)
		}
		return okEnvelope(upstreamResponse{Body: updated})
	case "already_present":
		counters.already.Add(1)
		logHost(req.HostCallbackID, "debug", "Claude identity system prompt already present", fields)
	case "cloak_skipped":
		counters.cloakSkipped.Add(1)
		logHost(req.HostCallbackID, "info", "Claude identity injection skipped because CPA Cloak is active", fields)
	}
	return okEnvelope(upstreamResponse{})
}

func firstMatchingRule(rules []rule, req upstreamRequest) *rule {
	for index := range rules {
		rule := &rules[index]
		if !rule.Enabled || !matchesRule(*rule, req) {
			continue
		}
		return rule
	}
	return nil
}

func matchesRule(rule rule, req upstreamRequest) bool {
	provider := req.Provider
	if provider == "" {
		provider = req.Auth.Provider
	}
	providerMatches := len(rule.ProviderAuthIndexes) > 0 && matchesExact(rule.ProviderAuthIndexes, req.Auth.Index)
	if len(rule.ProviderAuthIndexes) == 0 {
		providerMatches = len(rule.Providers) > 0 && matchesFold(rule.Providers, provider)
	}
	return (!matchEnabled(rule.MatchProviders) || providerMatches) &&
		(!matchEnabled(rule.MatchAuths) || ((len(rule.AuthIDs) > 0 || len(rule.AuthIndexes) > 0) && matchesExact(rule.AuthIDs, req.Auth.ID) && matchesExact(rule.AuthIndexes, req.Auth.Index))) &&
		(!matchEnabled(rule.MatchRequestedModels) || (len(rule.requestedPatterns) > 0 && matchesPatterns(rule.requestedPatterns, req.RequestedModel))) &&
		(!matchEnabled(rule.MatchUpstreamModels) || (len(rule.upstreamPatterns) > 0 && matchesPatterns(rule.upstreamPatterns, req.Model)))
}

func matchEnabled(value *bool) bool {
	return value != nil && *value
}

func matchesFold(values []string, actual string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(value, actual) {
			return true
		}
	}
	return false
}

func matchesExact(values []string, actual string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == actual {
			return true
		}
	}
	return false
}

func matchesPatterns(patterns []*regexp.Regexp, actual string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern.MatchString(actual) {
			return true
		}
	}
	return false
}

func injectIdentity(raw []byte, cloakHandling string) ([]byte, string, error) {
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil || body == nil {
		return nil, "", &injectError{message: "body must be a JSON object"}
	}
	identityRaw, _ := json.Marshal(systemBlock{Type: "text", Text: identityPrompt})
	rawSystem, exists := body["system"]
	if !exists || string(rawSystem) == "null" {
		body["system"] = json.RawMessage("[" + string(identityRaw) + "]")
		return marshalInjectedBody(body)
	}
	var systemText string
	if json.Unmarshal(rawSystem, &systemText) == nil {
		blocks := []json.RawMessage{identityRaw}
		if systemText != "" {
			original, _ := json.Marshal(systemBlock{Type: "text", Text: systemText})
			blocks = append(blocks, original)
		}
		replaced, _ := json.Marshal(blocks)
		body["system"] = replaced
		return marshalInjectedBody(body)
	}
	var blocks []json.RawMessage
	if errUnmarshal := json.Unmarshal(rawSystem, &blocks); errUnmarshal != nil {
		return nil, "", &injectError{message: "system must be a string or array"}
	}
	if len(blocks) > 0 {
		var first systemBlock
		if json.Unmarshal(blocks[0], &first) == nil && first.Text == identityPrompt {
			return nil, "already_present", nil
		}
	}
	if len(blocks) > 0 {
		var first systemBlock
		if json.Unmarshal(blocks[0], &first) == nil && strings.HasPrefix(first.Text, "x-anthropic-billing-header:") {
			for _, rawBlock := range blocks[1:] {
				var block systemBlock
				if json.Unmarshal(rawBlock, &block) == nil && block.Text == identityPrompt {
					return nil, "already_present", nil
				}
			}
			switch cloakHandling {
			case "skip":
				return nil, "cloak_skipped", nil
			case "compatible", "":
				blocks = append(blocks[:1], append([]json.RawMessage{identityRaw}, blocks[1:]...)...)
				replaced, _ := json.Marshal(blocks)
				body["system"] = replaced
				return marshalInjectedBody(body)
			}
		}
	}
	blocks = append([]json.RawMessage{identityRaw}, blocks...)
	replaced, _ := json.Marshal(blocks)
	body["system"] = replaced
	return marshalInjectedBody(body)
}

func marshalInjectedBody(body map[string]json.RawMessage) ([]byte, string, error) {
	updated, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, "", errMarshal
	}
	return updated, "injected", nil
}

type injectError struct{ message string }

func (e *injectError) Error() string { return e.message }

func logFields(req upstreamRequest, ruleID string) map[string]any {
	fields := map[string]any{
		"plugin":          pluginID,
		"provider":        req.Provider,
		"auth_index":      req.Auth.Index,
		"requested_model": req.RequestedModel,
		"upstream_model":  req.Model,
		"source_format":   req.SourceFormat,
	}
	if ruleID != "" {
		fields["rule_id"] = ruleID
	}
	return fields
}
