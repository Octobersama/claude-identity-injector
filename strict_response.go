package main

import (
	"encoding/json"
	"strings"
	"sync"
)

const streamChunkHeaderInitIndex = -1

type strictRequestState struct {
	CanonicalToClient map[string]string
	Schemas           map[string]map[string]any

	mu           sync.Mutex
	itemTools    map[string]string
	seenCalls    map[string]struct{}
	seenRestores map[string]struct{}
	seenFixes    map[string]struct{}
	seenIssues   map[string]struct{}
	responseSeen bool
}

var strictRequests = struct {
	sync.RWMutex
	values map[string]*strictRequestState
}{values: make(map[string]*strictRequestState)}

type requestCompletion struct {
	RequestID string `json:"RequestID"`
}

type responseInterceptRequest struct {
	RequestID      string `json:"RequestID"`
	SourceFormat   string `json:"SourceFormat"`
	Model          string `json:"Model"`
	Body           []byte `json:"Body"`
	HostCallbackID string `json:"host_callback_id"`
}

type responseInterceptResponse struct {
	Body []byte `json:"Body,omitempty"`
}

type streamChunkInterceptRequest struct {
	RequestID      string `json:"RequestID"`
	SourceFormat   string `json:"SourceFormat"`
	Model          string `json:"Model"`
	Body           []byte `json:"Body"`
	ChunkIndex     int    `json:"ChunkIndex"`
	HostCallbackID string `json:"host_callback_id"`
}

type streamChunkInterceptResponse struct {
	Body []byte `json:"Body,omitempty"`
}

func storeStrictRequest(req upstreamRequest, mapping strictToolMapping) {
	if strings.TrimSpace(req.RequestID) == "" {
		logHost(req.HostCallbackID, "debug", "Claude strict response tracking unavailable without request ID", logFields(req, ""))
		return
	}
	canonicalToClient := make(map[string]string, len(mapping.CanonicalToClient))
	for canonical, client := range mapping.CanonicalToClient {
		canonicalToClient[canonical] = client
	}
	state := &strictRequestState{
		CanonicalToClient: canonicalToClient,
		Schemas:           toolSchemasFromRequest(req.Body),
		itemTools:         make(map[string]string),
		seenCalls:         make(map[string]struct{}),
		seenRestores:      make(map[string]struct{}),
		seenFixes:         make(map[string]struct{}),
		seenIssues:        make(map[string]struct{}),
	}
	strictRequests.Lock()
	strictRequests.values[req.RequestID] = state
	strictRequests.Unlock()
	fields := logFields(req, "")
	fields["tool_schema_count"] = len(state.Schemas)
	fields["tool_alias_count"] = len(state.CanonicalToClient)
	logHost(req.HostCallbackID, "debug", "Claude strict response tracking registered", fields)
}

func strictRequest(requestID string) *strictRequestState {
	strictRequests.RLock()
	defer strictRequests.RUnlock()
	return strictRequests.values[requestID]
}

func deleteStrictRequest(requestID string) {
	if requestID == "" {
		return
	}
	strictRequests.Lock()
	delete(strictRequests.values, requestID)
	strictRequests.Unlock()
}

func clearStrictRequests() {
	strictRequests.Lock()
	strictRequests.values = make(map[string]*strictRequestState)
	strictRequests.Unlock()
}

func strictRequestCount() int {
	strictRequests.RLock()
	defer strictRequests.RUnlock()
	return len(strictRequests.values)
}

func (state *strictRequestState) rememberItemTool(itemID, tool string) {
	if state == nil || itemID == "" || tool == "" {
		return
	}
	state.mu.Lock()
	state.itemTools[itemID] = tool
	state.mu.Unlock()
}

func (state *strictRequestState) itemTool(itemID string) string {
	if state == nil || itemID == "" {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.itemTools[itemID]
}

func (state *strictRequestState) claim(seen map[string]struct{}, key string) bool {
	if state == nil || key == "" {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, exists := seen[key]; exists {
		return false
	}
	seen[key] = struct{}{}
	return true
}

func (state *strictRequestState) claimCall(key string) bool {
	return state.claim(state.seenCalls, key)
}

func (state *strictRequestState) claimRestore(key string) bool {
	return state.claim(state.seenRestores, key)
}

func (state *strictRequestState) claimFix(key string) bool {
	return state.claim(state.seenFixes, key)
}

func (state *strictRequestState) claimIssue(key string) bool {
	return state.claim(state.seenIssues, key)
}

func (state *strictRequestState) claimResponse() bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.responseSeen {
		return false
	}
	state.responseSeen = true
	return true
}

func logStrictResponseObserved(callbackID, requestID, sourceFormat, model string, stream bool, state *strictRequestState) {
	if state == nil || !state.claimResponse() {
		return
	}
	logHost(callbackID, "debug", "Claude strict response tracking correlated", map[string]any{
		"request_id": requestID, "source_format": sourceFormat, "model": model, "stream": stream,
		"tool_schema_count": len(state.Schemas),
	})
}

func handleRequestComplete(raw []byte) ([]byte, error) {
	var completion requestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	deleteStrictRequest(completion.RequestID)
	return okEnvelope(struct{}{})
}

func handleResponseIntercept(raw []byte) ([]byte, error) {
	var req responseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	state := strictRequest(req.RequestID)
	if state == nil || len(req.Body) == 0 {
		return okEnvelope(responseInterceptResponse{})
	}
	logStrictResponseObserved(req.HostCallbackID, req.RequestID, req.SourceFormat, req.Model, false, state)
	updated, report := repairStrictResponse(req.Body, req.SourceFormat, state)
	recordResponseRepair(req.HostCallbackID, req.RequestID, req.SourceFormat, req.Model, false, report)
	if !report.Changed {
		return okEnvelope(responseInterceptResponse{})
	}
	return okEnvelope(responseInterceptResponse{Body: updated})
}

func handleStreamChunkIntercept(raw []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	state := strictRequest(req.RequestID)
	if state == nil || req.ChunkIndex == streamChunkHeaderInitIndex || len(req.Body) == 0 {
		return okEnvelope(streamChunkInterceptResponse{})
	}
	logStrictResponseObserved(req.HostCallbackID, req.RequestID, req.SourceFormat, req.Model, true, state)
	updated, report := repairStrictResponse(req.Body, req.SourceFormat, state)
	recordResponseRepair(req.HostCallbackID, req.RequestID, req.SourceFormat, req.Model, true, report)
	if !report.Changed {
		return okEnvelope(streamChunkInterceptResponse{})
	}
	return okEnvelope(streamChunkInterceptResponse{Body: updated})
}
