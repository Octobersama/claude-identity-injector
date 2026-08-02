package main

import (
	"strings"
	"sync"
)

type requestMetricState struct {
	classified bool
	matched    bool
	outcomes   map[string]struct{}
}

var requestMetrics = struct {
	mu     sync.Mutex
	values map[string]*requestMetricState
}{values: make(map[string]*requestMetricState)}

func observeRequestPhase(req upstreamRequest, matched bool) {
	counters.interceptCalls.Add(1)
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		if req.Phase != "final" {
			return
		}
		counters.seen.Add(1)
		if matched {
			counters.matched.Add(1)
		} else {
			counters.unmatched.Add(1)
		}
		return
	}

	requestMetrics.mu.Lock()
	state := requestMetrics.values[requestID]
	if state == nil {
		state = &requestMetricState{outcomes: make(map[string]struct{})}
		requestMetrics.values[requestID] = state
		counters.seen.Add(1)
	}
	if req.Phase == "final" {
		classifyRequestMetric(state, matched)
	}
	requestMetrics.mu.Unlock()
}

func classifyRequestMetric(state *requestMetricState, matched bool) {
	if !state.classified {
		state.classified = true
		state.matched = matched
		if matched {
			counters.matched.Add(1)
		} else {
			counters.unmatched.Add(1)
		}
		return
	}
	if matched && !state.matched {
		state.matched = true
		counters.unmatched.Add(-1)
		counters.matched.Add(1)
	}
}

func recordRequestMetric(req upstreamRequest, metric string) bool {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return true
	}
	requestMetrics.mu.Lock()
	defer requestMetrics.mu.Unlock()
	state := requestMetrics.values[requestID]
	if state == nil {
		state = &requestMetricState{outcomes: make(map[string]struct{})}
		requestMetrics.values[requestID] = state
		counters.seen.Add(1)
	}
	if state.outcomes == nil {
		state.outcomes = make(map[string]struct{})
	}
	if _, exists := state.outcomes[metric]; exists {
		return false
	}
	state.outcomes[metric] = struct{}{}
	return true
}

func deleteRequestMetrics(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	requestMetrics.mu.Lock()
	delete(requestMetrics.values, requestID)
	requestMetrics.mu.Unlock()
}

func clearRequestMetrics() {
	requestMetrics.mu.Lock()
	requestMetrics.values = make(map[string]*requestMetricState)
	requestMetrics.mu.Unlock()
}
