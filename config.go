package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type config struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	Priority        int    `yaml:"priority" json:"priority"`
	Active          bool   `yaml:"active" json:"active"`
	CloakHandling   string `yaml:"cloak_handling" json:"cloak_handling"`
	SkipWhenCloaked *bool  `yaml:"skip_when_cloaked" json:"skip_when_cloaked"`
	LogMatches      *bool  `yaml:"log_matches" json:"log_matches"`
	Rules           []rule `yaml:"rules" json:"rules"`
}

type rule struct {
	ID                   string   `yaml:"id" json:"id"`
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	StrictMode           bool     `yaml:"strict_mode" json:"strict_mode"`
	StrictProfile        string   `yaml:"strict_profile,omitempty" json:"strict_profile,omitempty"`
	MatchProviders       *bool    `yaml:"match_providers" json:"match_providers"`
	MatchAuths           *bool    `yaml:"match_auths" json:"match_auths"`
	MatchRequestedModels *bool    `yaml:"match_requested_models" json:"match_requested_models"`
	MatchUpstreamModels  *bool    `yaml:"match_upstream_models" json:"match_upstream_models"`
	Providers            []string `yaml:"providers" json:"providers"`
	ProviderAuthIndexes  []string `yaml:"provider_auth_indexes" json:"provider_auth_indexes"`
	AuthIDs              []string `yaml:"auth_ids" json:"auth_ids"`
	AuthIndexes          []string `yaml:"auth_indexes" json:"auth_indexes"`
	RequestedModels      []string `yaml:"requested_models" json:"requested_models"`
	UpstreamModels       []string `yaml:"upstream_models" json:"upstream_models"`

	requestedPatterns []*regexp.Regexp
	upstreamPatterns  []*regexp.Regexp
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      registrationMetadata     `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationMetadata struct {
	Name             string `json:"Name"`
	Version          string `json:"Version"`
	Author           string `json:"Author"`
	GitHubRepository string `json:"GitHubRepository"`
}

type registrationCapabilities struct {
	UpstreamRequestInterceptor bool `json:"upstream_request_interceptor"`
	RequestLifecyclePlugin     bool `json:"request_lifecycle_plugin"`
	ResponseInterceptor        bool `json:"response_interceptor"`
	StreamChunkInterceptor     bool `json:"response_stream_interceptor"`
	ManagementAPI              bool `json:"management_api"`
}

var configState = struct {
	sync.RWMutex
	value config
}{value: defaultConfig()}

func defaultConfig() config {
	skip := true
	logs := true
	return config{
		Enabled:         true,
		Priority:        100,
		Active:          false,
		CloakHandling:   "compatible",
		SkipWhenCloaked: &skip,
		LogMatches:      &logs,
		Rules:           []rule{},
	}
}

func currentConfig() config {
	configState.RLock()
	defer configState.RUnlock()
	return configState.value
}

func handleLifecycle(method string, raw []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
		}
	}
	next, errParse := parseConfig(req.ConfigYAML)
	if errParse != nil {
		logHost("", "error", "Claude identity injector rejected invalid configuration", map[string]any{"error": errParse.Error()})
	} else {
		configState.Lock()
		configState.value = next
		configState.Unlock()
		logHost("", "info", "Claude identity injector configuration applied", map[string]any{
			"active": next.Active,
			"rules":  len(next.Rules),
			"method": method,
		})
	}
	return okEnvelope(pluginRegistration())
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: registrationMetadata{
			Name:             "Claude Identity Injector",
			Version:          pluginVersion,
			Author:           "Octobersama",
			GitHubRepository: "https://github.com/Octobersama/claude-identity-injector",
		},
		Capabilities: registrationCapabilities{
			UpstreamRequestInterceptor: true,
			RequestLifecyclePlugin:     true,
			ResponseInterceptor:        true,
			StreamChunkInterceptor:     true,
			ManagementAPI:              true,
		},
	}
}

func parseConfig(raw []byte) (config, error) {
	next := defaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return next, nil
	}
	if errUnmarshal := yaml.Unmarshal(raw, &next); errUnmarshal != nil {
		return config{}, fmt.Errorf("decode config YAML: %w", errUnmarshal)
	}
	if next.SkipWhenCloaked == nil {
		value := true
		next.SkipWhenCloaked = &value
	}
	if next.LogMatches == nil {
		value := true
		next.LogMatches = &value
	}
	next.CloakHandling = strings.ToLower(strings.TrimSpace(next.CloakHandling))
	if next.CloakHandling == "" {
		if next.SkipWhenCloaked != nil && *next.SkipWhenCloaked {
			next.CloakHandling = "skip"
		} else {
			next.CloakHandling = "prepend"
		}
	}
	if next.CloakHandling != "compatible" && next.CloakHandling != "skip" && next.CloakHandling != "prepend" {
		return config{}, fmt.Errorf("cloak_handling must be compatible, skip, or prepend")
	}
	seen := make(map[string]struct{}, len(next.Rules))
	for index := range next.Rules {
		rule := &next.Rules[index]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			return config{}, fmt.Errorf("rules[%d].id is required", index)
		}
		if _, exists := seen[rule.ID]; exists {
			return config{}, fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		rule.Providers = cleanList(rule.Providers)
		rule.ProviderAuthIndexes = cleanList(rule.ProviderAuthIndexes)
		rule.AuthIDs = cleanList(rule.AuthIDs)
		rule.AuthIndexes = cleanList(rule.AuthIndexes)
		rule.RequestedModels = cleanList(rule.RequestedModels)
		rule.UpstreamModels = cleanList(rule.UpstreamModels)
		rule.StrictProfile = normalizeStrictProfile(rule.StrictProfile)
		if !validStrictProfile(rule.StrictProfile) {
			return config{}, fmt.Errorf("rule %q strict_profile must be minimum, minimal, bearer, bearer_http1, minimal_core, identity, system, body, body_core, headers, headers_soft, body_headers, body_headers_core, or full", rule.ID)
		}
		setMatchDefault(&rule.MatchProviders, len(rule.ProviderAuthIndexes) > 0 || len(rule.Providers) > 0)
		setMatchDefault(&rule.MatchAuths, len(rule.AuthIDs) > 0 || len(rule.AuthIndexes) > 0)
		setMatchDefault(&rule.MatchRequestedModels, len(rule.RequestedModels) > 0)
		setMatchDefault(&rule.MatchUpstreamModels, len(rule.UpstreamModels) > 0)
		var errCompile error
		rule.requestedPatterns, errCompile = compileGlobs(rule.RequestedModels)
		if errCompile != nil {
			return config{}, fmt.Errorf("rule %q requested_models: %w", rule.ID, errCompile)
		}
		rule.upstreamPatterns, errCompile = compileGlobs(rule.UpstreamModels)
		if errCompile != nil {
			return config{}, fmt.Errorf("rule %q upstream_models: %w", rule.ID, errCompile)
		}
	}
	return next, nil
}

func setMatchDefault(target **bool, value bool) {
	if *target == nil {
		*target = new(bool)
		**target = value
	}
}

func normalizeStrictProfile(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "full"
	}
	return value
}

func validStrictProfile(value string) bool {
	switch normalizeStrictProfile(value) {
	case "minimum", "minimal", "bearer", "bearer_http1", "minimal_core", "identity", "system", "body", "body_core", "headers", "headers_soft", "body_headers", "body_headers_core", "full":
		return true
	default:
		return false
	}
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func compileGlobs(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		var expression strings.Builder
		expression.WriteString("^")
		for _, char := range pattern {
			switch char {
			case '*':
				expression.WriteString(".*")
			case '?':
				expression.WriteString(".")
			default:
				expression.WriteString(regexp.QuoteMeta(string(char)))
			}
		}
		expression.WriteString("$")
		compiled, errCompile := regexp.Compile(expression.String())
		if errCompile != nil {
			return nil, errCompile
		}
		out = append(out, compiled)
	}
	return out, nil
}
