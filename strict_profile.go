package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

const (
	strictClaudeVersion = "2.1.220"
	strictClaudeBuild   = "685"
	strictSDKVersion    = "0.94.0"
	strictRuntime       = "v26.3.0"
	strictBetas         = "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,fallback-credit-2026-06-01"
	strictCorePrompt    = `You are an interactive agent that helps users with software engineering tasks.

# Harness
- Text outside tool use is displayed to the user as GitHub-flavored Markdown in a terminal.
- Tools run behind a user-selected permission mode. Respect denied tool calls and adjust your approach.
- System reminders and tool results may add context during the conversation.
- Prefer dedicated file and search tools when available, and preserve the user's existing work.

# Doing tasks
- Read relevant code before changing it and match the surrounding style.
- Focus on the requested task, diagnose failures before changing approach, and avoid speculative changes.
- Write safe, correct code and report outcomes faithfully.

# Tone and style
- Be concise and direct.
- Reference code with file_path:line_number when useful.`
)

type strictTextBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *strictCacheControl `json:"cache_control,omitempty"`
}

type strictCacheControl struct {
	Type string `json:"type"`
}

func applyStrictClaudeCodeProfile(req upstreamRequest) ([]byte, http.Header, []string, error) {
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil || body == nil {
		return nil, nil, nil, fmt.Errorf("body must be a JSON object")
	}
	sessionID := strictSessionID(req)
	billing := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli;", strictClaudeVersion, strictClaudeBuild)
	system, _ := json.Marshal([]strictTextBlock{
		{Type: "text", Text: billing},
		{Type: "text", Text: identityPrompt, CacheControl: &strictCacheControl{Type: "ephemeral"}},
		{Type: "text", Text: strictCorePrompt, CacheControl: &strictCacheControl{Type: "ephemeral"}},
	})
	body["system"] = system
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
	updated, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, nil, nil, errMarshal
	}
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
	return updated, headers, []string{"x-client-request-id"}, nil
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

func strictBetasSHA256() string {
	sum := sha256.Sum256([]byte(strictBetas))
	return hex.EncodeToString(sum[:])
}
