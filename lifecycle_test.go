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
	if !registrationEnvelope.OK || registrationEnvelope.Result.SchemaVersion != schemaVersion || !registrationEnvelope.Result.Capabilities.UpstreamRequestInterceptor || !registrationEnvelope.Result.Capabilities.ResponseBeforeTranslator || !registrationEnvelope.Result.Capabilities.ResponseAfterTranslator || !registrationEnvelope.Result.Capabilities.ManagementAPI {
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
