package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvalidReconfigurePreservesLastValidConfig(t *testing.T) {
	valid, errParse := parseConfig([]byte("active: true\nrules:\n  - id: keep\n    enabled: true\n"))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	configState.Lock()
	previous := configState.value
	configState.value = valid
	configState.Unlock()
	t.Cleanup(func() {
		configState.Lock()
		configState.value = previous
		configState.Unlock()
	})

	req, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte("rules:\n  - id: duplicate\n  - id: duplicate\n"), SchemaVersion: schemaVersion})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if _, errHandle := handleLifecycle("plugin.reconfigure", req); errHandle != nil {
		t.Fatalf("handleLifecycle() error = %v", errHandle)
	}
	got := currentConfig()
	if !got.Active || len(got.Rules) != 1 || got.Rules[0].ID != "keep" {
		t.Fatalf("current config = %#v, want prior valid config", got)
	}
}

func TestParseConfigStrictProfiles(t *testing.T) {
	profiles := []string{"minimal", "bearer", "bearer_http1", "minimal_core", "identity", "system", "body", "body_core", "headers", "headers_soft", "body_headers", "body_headers_core", "full"}
	for _, profile := range profiles {
		t.Run(profile, func(t *testing.T) {
			raw := []byte("rules:\n  - id: profile\n    strict_profile: " + profile + "\n")
			parsed, errParse := parseConfig(raw)
			if errParse != nil {
				t.Fatalf("parseConfig() error = %v", errParse)
			}
			if got := parsed.Rules[0].StrictProfile; got != profile {
				t.Fatalf("strict profile = %q, want %q", got, profile)
			}
		})
	}
}

func TestParseConfigStrictProfileDefaultsToFull(t *testing.T) {
	parsed, errParse := parseConfig([]byte("rules:\n  - id: legacy\n"))
	if errParse != nil {
		t.Fatalf("parseConfig() error = %v", errParse)
	}
	if got := parsed.Rules[0].StrictProfile; got != "full" {
		t.Fatalf("strict profile = %q, want full", got)
	}
}

func TestParseConfigRejectsUnknownStrictProfile(t *testing.T) {
	_, errParse := parseConfig([]byte("rules:\n  - id: invalid\n    strict_profile: unknown\n"))
	if errParse == nil || !strings.Contains(errParse.Error(), "strict_profile") {
		t.Fatalf("parseConfig() error = %v, want strict_profile validation error", errParse)
	}
}

func TestRegistrationAndSettingsResource(t *testing.T) {
	registrationRaw, errHandle := handleMethod("plugin.register", nil)
	if errHandle != nil {
		t.Fatalf("plugin.register error = %v", errHandle)
	}
	var registrationEnvelope struct {
		OK     bool         `json:"ok"`
		Result registration `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(registrationRaw, &registrationEnvelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal(registration) error = %v", errUnmarshal)
	}
	capabilities := registrationEnvelope.Result.Capabilities
	if !registrationEnvelope.OK || registrationEnvelope.Result.SchemaVersion != schemaVersion || !capabilities.UpstreamRequestInterceptor || !capabilities.RequestLifecyclePlugin || !capabilities.ResponseInterceptor || !capabilities.StreamChunkInterceptor || !capabilities.ManagementAPI {
		t.Fatalf("registration = %#v", registrationEnvelope)
	}

	managementRaw, errHandle := handleMethod("management.register", nil)
	if errHandle != nil {
		t.Fatalf("management.register error = %v", errHandle)
	}
	var managementEnvelope struct {
		OK     bool                           `json:"ok"`
		Result managementRegistrationResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(managementRaw, &managementEnvelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal(management registration) error = %v", errUnmarshal)
	}
	if !managementEnvelope.OK || len(managementEnvelope.Result.Resources) != 1 || managementEnvelope.Result.Resources[0].Path != "/settings" {
		t.Fatalf("management registration = %#v", managementEnvelope)
	}

	request, _ := json.Marshal(managementRequest{Method: "GET", Path: "/plugins/" + pluginID + "/settings"})
	settingsRaw, errHandle := handleMethod("management.handle", request)
	if errHandle != nil {
		t.Fatalf("management.handle error = %v", errHandle)
	}
	var settingsEnvelope struct {
		OK     bool               `json:"ok"`
		Result managementResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(settingsRaw, &settingsEnvelope); errUnmarshal != nil {
		t.Fatalf("Unmarshal(settings response) error = %v", errUnmarshal)
	}
	if !settingsEnvelope.OK || settingsEnvelope.Result.StatusCode != 200 || !strings.Contains(string(settingsEnvelope.Result.Body), identityPrompt) {
		t.Fatalf("settings response status/body invalid: status=%d", settingsEnvelope.Result.StatusCode)
	}
}
