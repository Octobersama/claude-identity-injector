package main

import (
	"encoding/json"
	"strings"
	"unicode"
)

var strictCoreToolNames = []string{"Bash", "Edit", "Glob", "Grep", "Read", "Write"}

var strictCoreToolAliases = map[string][]string{
	"Bash":  {"bash", "shell", "terminal", "exec", "execute", "runshell", "runcommand", "shellcommand"},
	"Edit":  {"edit", "patch", "applypatch", "modifyfile", "replacefile"},
	"Glob":  {"glob", "findfiles", "listfiles", "fileglob"},
	"Grep":  {"grep", "search", "searchfiles", "searchtext", "findtext"},
	"Read":  {"read", "readfile", "getfile"},
	"Write": {"write", "writefile", "createfile"},
}

type strictToolMapping struct {
	Strategy          string
	ClientToolCount   int
	UpstreamToolCount int
	AliasCount        int
	FallbackCount     int
	CoreToolCount     int
	CanonicalToClient map[string]string
	ClientToCanonical map[string]string
}

type strictToolCandidate struct {
	index    int
	name     string
	object   map[string]json.RawMessage
	assigned []string
}

func applyStrictClientToolMapping(body map[string]json.RawMessage, sourceFormat string) (strictToolMapping, error) {
	rawTools := body["tools"]
	if strictToolsMissing(rawTools) {
		body["tools"] = json.RawMessage(strictToolsJSON)
		return strictToolMapping{
			Strategy:          "injected_core",
			UpstreamToolCount: len(strictCoreToolNames),
			CoreToolCount:     len(strictCoreToolNames),
		}, nil
	}

	if strings.EqualFold(strings.TrimSpace(sourceFormat), "claude") {
		count := strictToolCount(rawTools)
		return strictToolMapping{
			Strategy:          "preserved_native",
			ClientToolCount:   count,
			UpstreamToolCount: count,
			CoreToolCount:     strictRecognizedCoreToolCount(rawTools),
		}, nil
	}

	mappedTools, mapping, errMap := buildStrictClientToolMapping(rawTools)
	if errMap != nil {
		return strictToolMapping{}, errMap
	}
	if mapping.ClientToolCount == 0 {
		body["tools"] = json.RawMessage(strictToolsJSON)
		mapping.Strategy = "injected_core"
		mapping.UpstreamToolCount = len(strictCoreToolNames)
		mapping.CoreToolCount = len(strictCoreToolNames)
		return mapping, nil
	}
	body["tools"] = mappedTools
	rewriteStrictRequestToolReferences(body, mapping.ClientToCanonical)
	return mapping, nil
}

func buildStrictClientToolMapping(rawTools json.RawMessage) (json.RawMessage, strictToolMapping, error) {
	var tools []json.RawMessage
	if errUnmarshal := json.Unmarshal(rawTools, &tools); errUnmarshal != nil {
		return nil, strictToolMapping{}, errUnmarshal
	}

	candidates := make([]*strictToolCandidate, 0, len(tools))
	for index, rawTool := range tools {
		var object map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(rawTool, &object); errUnmarshal != nil || object == nil {
			continue
		}
		var name string
		if errUnmarshal := json.Unmarshal(object["name"], &name); errUnmarshal != nil || strings.TrimSpace(name) == "" {
			continue
		}
		candidates = append(candidates, &strictToolCandidate{index: index, name: name, object: object})
	}

	mapping := strictToolMapping{
		Strategy:          "client_tools",
		ClientToolCount:   len(candidates),
		UpstreamToolCount: len(tools),
		CanonicalToClient: make(map[string]string, len(strictCoreToolNames)),
		ClientToCanonical: make(map[string]string, len(strictCoreToolNames)),
	}
	if len(candidates) == 0 {
		return rawTools, mapping, nil
	}

	assignedCanonical := make(map[string]bool, len(strictCoreToolNames))
	usedCandidate := make(map[int]bool, len(candidates))
	assign := func(canonical string, candidateIndex int, fallback bool) {
		candidate := candidates[candidateIndex]
		candidate.assigned = append(candidate.assigned, canonical)
		assignedCanonical[canonical] = true
		usedCandidate[candidateIndex] = true
		mapping.CanonicalToClient[canonical] = candidate.name
		if _, exists := mapping.ClientToCanonical[candidate.name]; !exists {
			mapping.ClientToCanonical[candidate.name] = canonical
		}
		if canonical != candidate.name {
			mapping.AliasCount++
		}
		if fallback {
			mapping.FallbackCount++
		}
	}

	for _, canonical := range strictCoreToolNames {
		for candidateIndex, candidate := range candidates {
			if usedCandidate[candidateIndex] || !strictToolNameMatchesCanonical(candidate.name, canonical) {
				continue
			}
			assign(canonical, candidateIndex, false)
			break
		}
	}
	mapping.CoreToolCount = len(assignedCanonical)

	output := append([]json.RawMessage(nil), tools...)
	for _, candidate := range candidates {
		if len(candidate.assigned) == 0 {
			continue
		}
		primary := cloneRawObject(candidate.object)
		primary["name"], _ = json.Marshal(candidate.assigned[0])
		primaryRaw, errMarshal := json.Marshal(primary)
		if errMarshal != nil {
			return nil, strictToolMapping{}, errMarshal
		}
		output[candidate.index] = primaryRaw

		for _, canonical := range candidate.assigned[1:] {
			alias := cloneRawObject(candidate.object)
			alias["name"], _ = json.Marshal(canonical)
			delete(alias, "cache_control")
			aliasRaw, errMarshalAlias := json.Marshal(alias)
			if errMarshalAlias != nil {
				return nil, strictToolMapping{}, errMarshalAlias
			}
			output = append(output, aliasRaw)
		}
	}

	updated, errMarshal := json.Marshal(output)
	if errMarshal != nil {
		return nil, strictToolMapping{}, errMarshal
	}
	mapping.UpstreamToolCount = len(output)
	return updated, mapping, nil
}

func strictRecognizedCoreToolCount(rawTools json.RawMessage) int {
	var tools []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(rawTools, &tools) != nil {
		return 0
	}
	seen := make(map[string]bool, len(strictCoreToolNames))
	for _, tool := range tools {
		for _, canonical := range strictCoreToolNames {
			if strictToolNameMatchesCanonical(tool.Name, canonical) {
				seen[canonical] = true
				break
			}
		}
	}
	return len(seen)
}

func strictToolNameMatchesCanonical(name, canonical string) bool {
	normalized := normalizeStrictToolName(name)
	if normalized == normalizeStrictToolName(canonical) {
		return true
	}
	for _, alias := range strictCoreToolAliases[canonical] {
		if normalized == normalizeStrictToolName(alias) {
			return true
		}
	}
	return false
}

func normalizeStrictToolName(name string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func cloneRawObject(input map[string]json.RawMessage) map[string]json.RawMessage {
	cloned := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func strictToolCount(raw json.RawMessage) int {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return 0
	}
	return len(tools)
}

func rewriteStrictRequestToolReferences(body map[string]json.RawMessage, clientToCanonical map[string]string) {
	if len(clientToCanonical) == 0 {
		return
	}
	if rawChoice, exists := body["tool_choice"]; exists {
		var choice map[string]any
		if json.Unmarshal(rawChoice, &choice) == nil {
			if name, ok := choice["name"].(string); ok {
				if canonical, mapped := clientToCanonical[name]; mapped {
					choice["name"] = canonical
					body["tool_choice"], _ = json.Marshal(choice)
				}
			}
		}
	}
	if rawMessages, exists := body["messages"]; exists {
		var messages any
		if json.Unmarshal(rawMessages, &messages) == nil {
			rewriteStrictToolReferences(messages, clientToCanonical)
			body["messages"], _ = json.Marshal(messages)
		}
	}
}

func rewriteStrictToolReferences(value any, clientToCanonical map[string]string) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			rewriteStrictToolReferences(item, clientToCanonical)
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if itemType == "tool_use" {
			if name, ok := typed["name"].(string); ok {
				if canonical, mapped := clientToCanonical[name]; mapped {
					typed["name"] = canonical
				}
			}
		}
		if itemType == "tool_reference" {
			if name, ok := typed["tool_name"].(string); ok {
				if canonical, mapped := clientToCanonical[name]; mapped {
					typed["tool_name"] = canonical
				}
			}
		}
		for _, child := range typed {
			rewriteStrictToolReferences(child, clientToCanonical)
		}
	}
}

func (mapping strictToolMapping) logValue() string {
	if len(mapping.CanonicalToClient) == 0 {
		return ""
	}
	values := make([]string, 0, len(mapping.CanonicalToClient))
	for _, canonical := range strictCoreToolNames {
		if client, exists := mapping.CanonicalToClient[canonical]; exists {
			values = append(values, canonical+"<-"+client)
		}
	}
	return strings.Join(values, ",")
}
