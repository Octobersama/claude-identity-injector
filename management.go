package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

//go:embed web/settings.html
var settingsPage []byte

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"Routes,omitempty"`
	Resources []resourceRoute   `json:"Resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method         string              `json:"Method"`
	Path           string              `json:"Path"`
	Headers        map[string][]string `json:"Headers"`
	Body           []byte              `json:"Body"`
	HostCallbackID string              `json:"host_callback_id"`
}

type managementResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

type authListResponse struct {
	Files []authSummary `json:"files"`
}

type authSummary struct {
	ID          string   `json:"id,omitempty"`
	AuthIndex   string   `json:"auth_index,omitempty"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider,omitempty"`
	BaseURL     string   `json:"base_url,omitempty"`
	Models      []string `json:"models,omitempty"`
	Label       string   `json:"label,omitempty"`
	Status      string   `json:"status,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	Unavailable bool     `json:"unavailable,omitempty"`
	RuntimeOnly bool     `json:"runtime_only,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type providerListResponse struct {
	Providers []string `json:"providers"`
}

type modelListResponse struct {
	Models []modelSummary `json:"models"`
}

type modelSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + pluginID
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/auths", Description: "Non-sensitive runtime credential summaries."},
			{Method: http.MethodGet, Path: base + "/catalog", Description: "Available AI providers and upstream models."},
			{Method: http.MethodGet, Path: base + "/status", Description: "Plugin runtime counters and effective state."},
		},
		Resources: []resourceRoute{{
			Path:        "/settings",
			Menu:        "Claude 身份注入",
			Description: "按提供商、凭证和模型配置 Claude system 首位身份提示词。",
		}},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
	}
	path := strings.TrimRight(req.Path, "/")
	switch {
	case strings.HasSuffix(path, "/settings"):
		return okEnvelope(managementHTTPResponse(http.StatusOK, "text/html; charset=utf-8", settingsPage))
	case strings.HasSuffix(path, "/auths"):
		result, errCall := callHost("host.auth.list", map[string]any{"host_callback_id": req.HostCallbackID})
		if errCall != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errCall.Error()}))
		}
		var auths authListResponse
		if errUnmarshal := json.Unmarshal(result, &auths); errUnmarshal != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errUnmarshal.Error()}))
		}
		return okEnvelope(managementJSONResponse(http.StatusOK, auths))
	case strings.HasSuffix(path, "/catalog"):
		providersRaw, errProviders := callHost("host.provider.list", map[string]any{"host_callback_id": req.HostCallbackID})
		if errProviders != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errProviders.Error()}))
		}
		modelsRaw, errModels := callHost("host.model.list", map[string]any{"host_callback_id": req.HostCallbackID})
		if errModels != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errModels.Error()}))
		}
		var providers providerListResponse
		var models modelListResponse
		if errUnmarshal := json.Unmarshal(providersRaw, &providers); errUnmarshal != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errUnmarshal.Error()}))
		}
		if errUnmarshal := json.Unmarshal(modelsRaw, &models); errUnmarshal != nil {
			return okEnvelope(managementJSONResponse(http.StatusBadGateway, map[string]any{"error": errUnmarshal.Error()}))
		}
		return okEnvelope(managementJSONResponse(http.StatusOK, map[string]any{"providers": providers.Providers, "models": models.Models}))
	case strings.HasSuffix(path, "/status"):
		cfg := currentConfig()
		seen := counters.seen.Load()
		matched := counters.matched.Load()
		injected := counters.injected.Load()
		alreadyPresent := counters.already.Load()
		unmatched := uint64(0)
		if seen > matched {
			unmatched = seen - matched
		}
		return okEnvelope(managementJSONResponse(http.StatusOK, map[string]any{
			"active":          cfg.Active,
			"rules":           len(cfg.Rules),
			"identity":        identityPrompt,
			"seen":            seen,
			"matched":         matched,
			"unmatched":       unmatched,
			"injected":        injected,
			"already_present": alreadyPresent,
			"strict_takeover": counters.strict.Load(),
			"effective":       injected + alreadyPresent,
			"cloak_skipped":   counters.cloakSkipped.Load(),
			"errors":          counters.errors.Load(),
		}))
	default:
		return okEnvelope(managementJSONResponse(http.StatusNotFound, map[string]any{"error": "not_found"}))
	}
}

func managementHTTPResponse(status int, contentType string, body []byte) managementResponse {
	return managementResponse{
		StatusCode: status,
		Headers: map[string][]string{
			"content-type":  []string{contentType},
			"cache-control": []string{"no-store"},
		},
		Body: body,
	}
}

func managementJSONResponse(status int, value any) managementResponse {
	body, _ := json.Marshal(value)
	return managementHTTPResponse(status, "application/json; charset=utf-8", body)
}
