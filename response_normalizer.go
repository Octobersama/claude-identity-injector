package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

type responseTransformRequest struct {
	FromFormat        string `json:"FromFormat"`
	ToFormat          string `json:"ToFormat"`
	Model             string `json:"Model"`
	Stream            bool   `json:"Stream"`
	OriginalRequest   []byte `json:"OriginalRequest"`
	TranslatedRequest []byte `json:"TranslatedRequest"`
	Body              []byte `json:"Body"`
}

type payloadResponse struct {
	Body []byte `json:"Body,omitempty"`
}

func handleResponseNormalizeBefore(raw []byte) ([]byte, error) {
	var req responseTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if !currentConfig().Active || !strings.EqualFold(req.FromFormat, "claude") || strings.EqualFold(req.ToFormat, "claude") {
		return okEnvelope(payloadResponse{})
	}

	mapping, errMapping := strictToolMappingFromRequest(req.TranslatedRequest)
	if errMapping != nil {
		logHost("", "warn", "Claude client tool alias reconstruction failed", map[string]any{
			"from_format": req.FromFormat,
			"to_format":   req.ToFormat,
			"model":       req.Model,
			"stream":      req.Stream,
			"error":       errMapping.Error(),
		})
		return okEnvelope(payloadResponse{})
	}
	if len(mapping.CanonicalToClient) == 0 {
		return okEnvelope(payloadResponse{})
	}
	updated, restored := restoreStrictToolNames(req.Body, mapping.CanonicalToClient)
	if restored == 0 {
		return okEnvelope(payloadResponse{})
	}
	counters.toolNamesRestored.Add(uint64(restored))
	logHost("", "debug", "Claude client tool aliases restored", map[string]any{
		"from_format":  req.FromFormat,
		"to_format":    req.ToFormat,
		"model":        req.Model,
		"stream":       req.Stream,
		"restored":     restored,
		"tool_mapping": mapping.logValue(),
	})
	return okEnvelope(payloadResponse{Body: updated})
}

func strictToolMappingFromRequest(raw []byte) (strictToolMapping, error) {
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil || body == nil {
		return strictToolMapping{}, errUnmarshal
	}
	rawTools := body["tools"]
	if strictToolsMissing(rawTools) {
		return strictToolMapping{}, nil
	}
	_, mapping, errMapping := buildStrictClientToolMapping(rawTools)
	return mapping, errMapping
}

func restoreStrictToolNames(raw []byte, canonicalToClient map[string]string) ([]byte, int) {
	if len(raw) == 0 || len(canonicalToClient) == 0 {
		return raw, 0
	}
	if json.Valid(raw) {
		return restoreStrictToolNamesJSON(raw, canonicalToClient)
	}

	lines := bytes.SplitAfter(raw, []byte("\n"))
	restoredTotal := 0
	for index, line := range lines {
		lineEnding := []byte{}
		content := line
		if bytes.HasSuffix(content, []byte("\n")) {
			lineEnding = []byte("\n")
			content = content[:len(content)-1]
		}
		carriageReturn := []byte{}
		if bytes.HasSuffix(content, []byte("\r")) {
			carriageReturn = []byte("\r")
			content = content[:len(content)-1]
		}
		trimmed := bytes.TrimLeft(content, " \t")
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		prefixLength := len(content) - len(trimmed) + len("data:")
		payload := bytes.TrimSpace(content[prefixLength:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
			continue
		}
		updated, restored := restoreStrictToolNamesJSON(payload, canonicalToClient)
		if restored == 0 {
			continue
		}
		prefix := content[:prefixLength]
		rebuilt := make([]byte, 0, len(prefix)+1+len(updated)+len(carriageReturn)+len(lineEnding))
		rebuilt = append(rebuilt, prefix...)
		rebuilt = append(rebuilt, ' ')
		rebuilt = append(rebuilt, updated...)
		rebuilt = append(rebuilt, carriageReturn...)
		rebuilt = append(rebuilt, lineEnding...)
		lines[index] = rebuilt
		restoredTotal += restored
	}
	if restoredTotal == 0 {
		return raw, 0
	}
	return bytes.Join(lines, nil), restoredTotal
}

func restoreStrictToolNamesJSON(raw []byte, canonicalToClient map[string]string) ([]byte, int) {
	var value any
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return raw, 0
	}
	restored := restoreStrictToolNamesValue(value, canonicalToClient)
	if restored == 0 {
		return raw, 0
	}
	updated, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return raw, 0
	}
	return updated, restored
}

func restoreStrictToolNamesValue(value any, canonicalToClient map[string]string) int {
	restored := 0
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			restored += restoreStrictToolNamesValue(item, canonicalToClient)
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if itemType == "tool_use" {
			if name, ok := typed["name"].(string); ok {
				if clientName, mapped := canonicalToClient[name]; mapped && clientName != name {
					typed["name"] = clientName
					restored++
				}
			}
		}
		for _, child := range typed {
			restored += restoreStrictToolNamesValue(child, canonicalToClient)
		}
	}
	return restored
}
