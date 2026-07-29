package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
)

const identityPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

type upstreamRequest struct {
	RequestID      string       `json:"RequestID"`
	SourceFormat   string       `json:"SourceFormat"`
	ToFormat       string       `json:"ToFormat"`
	Provider       string       `json:"Provider"`
	Model          string       `json:"Model"`
	RequestedModel string       `json:"RequestedModel"`
	Stream         bool         `json:"Stream"`
	Auth           upstreamAuth `json:"Auth"`
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
	Body []byte `json:"Body,omitempty"`
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
	cloakSkipped atomic.Uint64
	errors       atomic.Uint64
}

func handleUpstreamIntercept(raw []byte) ([]byte, error) {
	var req upstreamRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	counters.seen.Add(1)
	cfg := currentConfig()
	if !cfg.Active || !strings.EqualFold(req.ToFormat, "claude") {
		return okEnvelope(upstreamResponse{})
	}
	matchedRule := firstMatchingRule(cfg.Rules, req)
	if matchedRule == nil {
		logHost(req.HostCallbackID, "debug", "Claude identity injection rule did not match", logFields(req, ""))
		return okEnvelope(upstreamResponse{})
	}
	counters.matched.Add(1)

	updated, outcome, errInject := injectIdentity(req.Body, cfg.SkipWhenCloaked != nil && *cfg.SkipWhenCloaked)
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
	return matchesFold(rule.Providers, provider) &&
		matchesExact(rule.AuthIDs, req.Auth.ID) &&
		matchesExact(rule.AuthIndexes, req.Auth.Index) &&
		matchesPatterns(rule.requestedPatterns, req.RequestedModel) &&
		matchesPatterns(rule.upstreamPatterns, req.Model)
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

func injectIdentity(raw []byte, skipWhenCloaked bool) ([]byte, string, error) {
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
		if json.Unmarshal(blocks[0], &first) == nil {
			if first.Text == identityPrompt {
				return nil, "already_present", nil
			}
			if skipWhenCloaked && strings.HasPrefix(first.Text, "x-anthropic-billing-header:") {
				return nil, "cloak_skipped", nil
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
