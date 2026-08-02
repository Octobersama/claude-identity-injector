package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type strictBodySummary struct {
	TopLevelKeys        string
	SystemBlocks        int
	SystemTypes         string
	MessageCount        int
	MessageRoles        string
	MessageContentTypes string
	ToolCount           int
	ToolNames           string
	MetadataKeys        string
	MetadataUserIDKeys  string
	HasThinking         bool
	HasContext          bool
	HasOutputConfig     bool
}

func summarizeStrictBody(raw []byte) strictBodySummary {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return strictBodySummary{}
	}

	summary := strictBodySummary{
		TopLevelKeys:    sortedMapKeys(body),
		HasThinking:     rawJSONPresent(body["thinking"]),
		HasContext:      rawJSONPresent(body["context_management"]),
		HasOutputConfig: rawJSONPresent(body["output_config"]),
	}

	var system []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body["system"], &system) == nil {
		summary.SystemBlocks = len(system)
		systemTypes := make([]string, 0, len(system))
		for _, block := range system {
			systemTypes = append(systemTypes, block.Type)
		}
		summary.SystemTypes = sortedUniqueStrings(systemTypes)
	}

	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body["messages"], &messages) == nil {
		summary.MessageCount = len(messages)
		roles := make([]string, 0, len(messages))
		contentTypes := make([]string, 0)
		for _, message := range messages {
			roles = append(roles, message.Role)
			contentTypes = append(contentTypes, messageContentTypes(message.Content)...)
		}
		summary.MessageRoles = sortedUniqueStrings(roles)
		summary.MessageContentTypes = sortedUniqueStrings(contentTypes)
	}

	var tools []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body["tools"], &tools) == nil {
		summary.ToolCount = len(tools)
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		summary.ToolNames = sortedUniqueStrings(names)
	}

	var metadata map[string]json.RawMessage
	if json.Unmarshal(body["metadata"], &metadata) == nil {
		summary.MetadataKeys = sortedMapKeys(metadata)
		var userID string
		if json.Unmarshal(metadata["user_id"], &userID) == nil {
			var userIDFields map[string]json.RawMessage
			if json.Unmarshal([]byte(userID), &userIDFields) == nil {
				summary.MetadataUserIDKeys = sortedMapKeys(userIDFields)
			}
		}
	}

	return summary
}

func (summary strictBodySummary) logFields() map[string]any {
	return map[string]any{
		"body_top_level_keys":    summary.TopLevelKeys,
		"system_blocks":          summary.SystemBlocks,
		"system_types":           summary.SystemTypes,
		"message_count":          summary.MessageCount,
		"message_roles":          summary.MessageRoles,
		"message_content_types":  summary.MessageContentTypes,
		"tool_count":             summary.ToolCount,
		"tool_names":             summary.ToolNames,
		"metadata_keys":          summary.MetadataKeys,
		"metadata_user_id_keys":  summary.MetadataUserIDKeys,
		"has_thinking":           summary.HasThinking,
		"has_context_management": summary.HasContext,
		"has_output_config":      summary.HasOutputConfig,
	}
}

func (summary strictBodySummary) compact() string {
	return strings.Join([]string{
		"body_shape=" + summary.TopLevelKeys,
		"system_blocks=" + formatInt(summary.SystemBlocks),
		"system_types=" + summary.SystemTypes,
		"message_count=" + formatInt(summary.MessageCount),
		"message_roles=" + summary.MessageRoles,
		"message_content_types=" + summary.MessageContentTypes,
		"tool_count=" + formatInt(summary.ToolCount),
		"tool_names=" + summary.ToolNames,
		"metadata_keys=" + summary.MetadataKeys,
		"metadata_user_id_keys=" + summary.MetadataUserIDKeys,
		"has_thinking=" + formatBool(summary.HasThinking),
		"has_context_management=" + formatBool(summary.HasContext),
		"has_output_config=" + formatBool(summary.HasOutputConfig),
	}, " ")
}

func messageContentTypes(raw json.RawMessage) []string {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		values := make([]string, 0, len(blocks))
		for _, block := range blocks {
			values = append(values, block.Type)
		}
		return values
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []string{"string"}
	}
	return nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null"
}

func sortedMapKeys[T any](values map[string]T) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func sortedUniqueStrings(values []string) string {
	seen := make(map[string]struct{}, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

func formatBool(value bool) string {
	return strconv.FormatBool(value)
}
