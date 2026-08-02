package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// strictHarnessPrompt is the exact Claude Code harness system prompt captured
// from the official client (system[2]).
//
//go:embed strict_harness.txt
var strictHarnessPrompt string

// strictToolsJSON is a minimal Claude Code core tool set (Bash, Edit, Glob,
// Grep, Read, Write) captured from the official client. Strict mode injects it
// only when the client did not send executable tools; otherwise client tool
// definitions are transported under these standard names.
//
//go:embed strict_tools.json
var strictToolsJSON []byte

// strictMinimumToolsJSON is the smallest read-only Claude Code tool subset
// that passed the AnyRouter fingerprint checks during ablation testing.
//
//go:embed strict_minimum_tools.json
var strictMinimumToolsJSON []byte

const (
	strictClaudeVersion = "2.1.220"
	strictClaudeBuild   = "685"
	strictSDKVersion    = "0.94.0"
	strictRuntime       = "v26.3.0"
	strictBetas         = "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,fallback-credit-2026-06-01"
	strictMinimumBetas  = "context-1m-2025-08-07"
)

type strictTextBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *strictCacheControl `json:"cache_control,omitempty"`
}

type strictCacheControl struct {
	Type string `json:"type"`
}

type strictProfileControls struct {
	Profile                    string
	MinimumFingerprint         bool
	IdentityOnly               bool
	FullSystem                 bool
	FullBody                   bool
	FullHeaders                bool
	SoftHeaders                bool
	InjectCoreTools            bool
	MapTools                   bool
	ForceBearerAuthorization   bool
	ForceAPIKeyAuthentication  bool
	ReplaceHeaders             bool
	SkipUpstreamBodyTransforms bool
	ForceHTTP1                 bool
	ClientSystemRelocated      bool
	ClientSystemBlocksMoved    int
	ClientSystemBytesMoved     int
}

func applyStrictClaudeCodeProfile(req upstreamRequest) ([]byte, http.Header, []string, strictToolMapping, error) {
	updated, headers, clearHeaders, mapping, _, err := applyStrictClaudeCodeProfileWithProfile(req, "full")
	return updated, headers, clearHeaders, mapping, err
}

func applyStrictClaudeCodeProfileWithProfile(req upstreamRequest, profile string) ([]byte, http.Header, []string, strictToolMapping, strictProfileControls, error) {
	controls := strictProfileFeatures(profile)
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil || body == nil {
		return nil, nil, nil, strictToolMapping{}, controls, fmt.Errorf("body must be a JSON object")
	}
	toolMapping := strictToolMapping{Strategy: "preserved_native", ClientToolCount: strictToolCount(body["tools"]), UpstreamToolCount: strictToolCount(body["tools"])}
	injectedCoreTools := false
	if controls.MinimumFingerprint {
		updated, _, errInject := injectIdentity(req.Body, "prepend")
		if errInject != nil {
			return nil, nil, nil, strictToolMapping{}, controls, errInject
		}
		if updated != nil {
			if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
				return nil, nil, nil, strictToolMapping{}, controls, errUnmarshal
			}
		}
		sessionID := strictSessionID(req)
		setStrictMinimumMetadata(body, strictMinimumUserID(req, sessionID))
		if !strings.EqualFold(strings.TrimSpace(req.SourceFormat), "claude") {
			relocation, errRelocate := relocateMinimumClientSystem(body)
			if errRelocate != nil {
				return nil, nil, nil, strictToolMapping{}, controls, fmt.Errorf("relocate client system: %w", errRelocate)
			}
			controls.ClientSystemRelocated = relocation.BlocksMoved > 0
			controls.ClientSystemBlocksMoved = relocation.BlocksMoved
			controls.ClientSystemBytesMoved = relocation.BytesMoved
		}
	}
	if controls.MapTools {
		if controls.MinimumFingerprint && strictToolsMissing(body["tools"]) {
			body["tools"] = json.RawMessage(strictMinimumToolsJSON)
			toolMapping = strictToolMapping{
				Strategy:          "injected_minimum",
				UpstreamToolCount: 3,
				CoreToolCount:     3,
			}
		} else {
			var errTools error
			toolMapping, errTools = applyStrictClientToolMapping(body, req.SourceFormat)
			if errTools != nil {
				return nil, nil, nil, strictToolMapping{}, controls, fmt.Errorf("map client tools: %w", errTools)
			}
		}
		if controls.MinimumFingerprint && toolMapping.CoreToolCount < 3 {
			var errTools error
			toolMapping, errTools = ensureStrictMinimumCoreTools(body, toolMapping)
			if errTools != nil {
				return nil, nil, nil, strictToolMapping{}, controls, fmt.Errorf("ensure minimum core tools: %w", errTools)
			}
		}
	} else if controls.InjectCoreTools && strictToolsMissing(body["tools"]) {
		body["tools"] = json.RawMessage(strictToolsJSON)
		injectedCoreTools = true
		toolMapping = strictToolMapping{
			Strategy:          "injected_core",
			UpstreamToolCount: len(strictCoreToolNames),
		}
	}
	if controls.IdentityOnly {
		updated, _, errInject := injectIdentity(req.Body, "prepend")
		if errInject != nil {
			return nil, nil, nil, strictToolMapping{}, controls, errInject
		}
		body = nil
		if errUnmarshal := json.Unmarshal(updated, &body); errUnmarshal != nil {
			return nil, nil, nil, strictToolMapping{}, controls, errUnmarshal
		}
	}
	if controls.FullSystem {
		billing := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli;", strictClaudeVersion, strictClaudeBuild)
		system, _ := json.Marshal([]strictTextBlock{
			{Type: "text", Text: billing},
			{Type: "text", Text: identityPrompt, CacheControl: &strictCacheControl{Type: "ephemeral"}},
			{Type: "text", Text: strictHarnessPrompt, CacheControl: &strictCacheControl{Type: "ephemeral"}},
		})
		body["system"] = system
		if controls.FullBody {
			sessionID := strictSessionID(req)
			metadata, _ := json.Marshal(map[string]string{"user_id": strictUserID(req, sessionID)})
			body["metadata"] = metadata
			if _, exists := body["thinking"]; !exists {
				body["thinking"] = json.RawMessage(`{"type":"adaptive"}`)
			}
			if _, exists := body["context_management"]; !exists {
				body["context_management"] = json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`)
			}
			if _, exists := body["output_config"]; !exists {
				body["output_config"] = json.RawMessage(`{"effort":"high"}`)
			}
		}
	}
	updated := req.Body
	var errMarshal error
	if controls.MinimumFingerprint || controls.IdentityOnly || controls.FullSystem || injectedCoreTools || controls.MapTools {
		updated, errMarshal = json.Marshal(body)
	}
	if errMarshal != nil {
		return nil, nil, nil, strictToolMapping{}, controls, errMarshal
	}
	if controls.MinimumFingerprint {
		return updated, http.Header{
			"anthropic-beta":    []string{strictMinimumBetas},
			"Anthropic-Version": []string{"2023-06-01"},
			"Content-Type":      []string{"application/json"},
			"User-Agent":        []string{fmt.Sprintf("claude-cli/%s (external, cli)", strictClaudeVersion)},
		}, nil, toolMapping, controls, nil
	}
	if !controls.FullHeaders {
		return updated, http.Header{"anthropic-beta": []string{strictBetas}}, nil, toolMapping, controls, nil
	}
	sessionID := strictSessionID(req)
	headers := http.Header{
		"Accept":          []string{"application/json"},
		"Accept-Encoding": []string{"gzip, deflate, br, zstd"},
		"anthropic-beta":  []string{strictBetas},
		"Anthropic-Dangerous-Direct-Browser-Access": []string{"true"},
		"Anthropic-Version":                         []string{"2023-06-01"},
		"Connection":                                []string{"keep-alive"},
		"Content-Type":                              []string{"application/json"},
		"User-Agent":                                []string{fmt.Sprintf("claude-cli/%s (external, cli)", strictClaudeVersion)},
		"X-App":                                     []string{"cli"},
		"X-Claude-Code-Session-Id":                  []string{sessionID},
		"X-Stainless-Arch":                          []string{"x64"},
		"X-Stainless-Lang":                          []string{"js"},
		"X-Stainless-OS":                            []string{"Windows"},
		"X-Stainless-Package-Version":               []string{strictSDKVersion},
		"X-Stainless-Retry-Count":                   []string{"0"},
		"X-Stainless-Runtime":                       []string{"node"},
		"X-Stainless-Runtime-Version":               []string{strictRuntime},
		"X-Stainless-Timeout":                       []string{"600"},
	}
	return updated, headers, []string{"x-client-request-id"}, toolMapping, controls, nil
}

func strictProfileFeatures(profile string) strictProfileControls {
	controls := strictProfileControls{
		Profile:                    normalizeStrictProfile(profile),
		SkipUpstreamBodyTransforms: true,
	}
	switch controls.Profile {
	case "minimum":
		controls.MinimumFingerprint = true
		controls.MapTools = true
		controls.ReplaceHeaders = true
	case "bearer":
		controls.ForceBearerAuthorization = true
	case "bearer_http1":
		controls.ForceBearerAuthorization = true
		controls.ForceHTTP1 = true
	case "minimal_core":
		controls.InjectCoreTools = true
	case "identity":
		controls.IdentityOnly = true
	case "system":
		controls.FullSystem = true
	case "body":
		controls.FullSystem = true
		controls.FullBody = true
	case "body_core":
		controls.FullSystem = true
		controls.FullBody = true
		controls.InjectCoreTools = true
	case "headers":
		controls.FullHeaders = true
	case "headers_soft":
		controls.FullHeaders = true
		controls.SoftHeaders = true
	case "body_headers":
		controls.FullSystem = true
		controls.FullBody = true
		controls.FullHeaders = true
	case "body_headers_core":
		controls.FullSystem = true
		controls.FullBody = true
		controls.FullHeaders = true
		controls.InjectCoreTools = true
	case "full":
		controls.FullSystem = true
		controls.FullBody = true
		controls.FullHeaders = true
		controls.MapTools = true
	}
	if controls.FullHeaders {
		controls.ReplaceHeaders = true
		if !controls.SoftHeaders {
			controls.ForceBearerAuthorization = true
			controls.ForceHTTP1 = true
		}
	}
	return controls
}

func strictToolsMissing(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var tools []json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &tools); errUnmarshal != nil {
		return true
	}
	return len(tools) == 0
}

func strictSessionID(req upstreamRequest) string {
	seed := req.Auth.Index + "\x00" + req.Auth.ID + "\x00claude-code-session"
	sum := sha256.Sum256([]byte(seed))
	hexValue := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:32])
}

func strictUserID(req upstreamRequest, sessionID string) string {
	device := sha256.Sum256([]byte(req.Auth.Index + "\x00" + req.Auth.ID + "\x00claude-code-device"))
	value, _ := json.Marshal(map[string]string{
		"device_id":    hex.EncodeToString(device[:]),
		"account_uuid": "",
		"session_id":   sessionID,
	})
	return string(value)
}

func strictMinimumUserID(req upstreamRequest, sessionID string) string {
	device := sha256.Sum256([]byte(req.Auth.Index + "\x00" + req.Auth.ID + "\x00claude-code-device"))
	value, _ := json.Marshal(map[string]string{
		"device_id":  hex.EncodeToString(device[:]),
		"session_id": sessionID,
	})
	return string(value)
}

func setStrictMinimumMetadata(body map[string]json.RawMessage, userID string) {
	metadata := make(map[string]json.RawMessage)
	if rawMetadata, exists := body["metadata"]; exists {
		_ = json.Unmarshal(rawMetadata, &metadata)
	}
	metadata["user_id"], _ = json.Marshal(userID)
	body["metadata"], _ = json.Marshal(metadata)
}

func ensureStrictMinimumCoreTools(body map[string]json.RawMessage, mapping strictToolMapping) (strictToolMapping, error) {
	var tools []json.RawMessage
	if rawTools := body["tools"]; !strictToolsMissing(rawTools) {
		if errUnmarshal := json.Unmarshal(rawTools, &tools); errUnmarshal != nil {
			return mapping, errUnmarshal
		}
	}
	var minimumTools []json.RawMessage
	if errUnmarshal := json.Unmarshal(strictMinimumToolsJSON, &minimumTools); errUnmarshal != nil {
		return mapping, errUnmarshal
	}
	seen := make(map[string]bool, len(strictCoreToolNames))
	for _, rawTool := range tools {
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(rawTool, &tool) != nil {
			continue
		}
		for _, canonical := range strictCoreToolNames {
			if strictToolNameMatchesCanonical(tool.Name, canonical) {
				seen[canonical] = true
				break
			}
		}
	}
	for _, rawTool := range minimumTools {
		if len(seen) >= 3 {
			break
		}
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(rawTool, &tool) != nil || seen[tool.Name] {
			continue
		}
		tools = append(tools, rawTool)
		seen[tool.Name] = true
	}
	updated, errMarshal := json.Marshal(tools)
	if errMarshal != nil {
		return mapping, errMarshal
	}
	body["tools"] = updated
	if mapping.ClientToolCount == 0 {
		mapping.Strategy = "injected_minimum"
	} else {
		mapping.Strategy = "client_tools_padded"
	}
	mapping.UpstreamToolCount = len(tools)
	mapping.CoreToolCount = len(seen)
	return mapping, nil
}

func strictBetasSHA256() string {
	return strictBetasValueSHA256(strictBetas)
}

func strictBetasValueSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
