package main

import "testing"

func resetRequestMetricTestState() {
	clearRequestMetrics()
	counters.seen.Store(0)
	counters.interceptCalls.Store(0)
	counters.matched.Store(0)
	counters.unmatched.Store(0)
	counters.injected.Store(0)
	counters.already.Store(0)
	counters.strict.Store(0)
	counters.toolMapped.Store(0)
	counters.cloakSkipped.Store(0)
	counters.errors.Store(0)
}

func TestRequestMetricsCountOneRequestAcrossPhases(t *testing.T) {
	resetRequestMetricTestState()
	t.Cleanup(resetRequestMetricTestState)

	req := upstreamRequest{RequestID: "request-1", Phase: "pre_cloak"}
	observeRequestPhase(req, false)
	req.Phase = "final"
	observeRequestPhase(req, true)

	if got := counters.interceptCalls.Load(); got != 2 {
		t.Fatalf("intercept calls = %d, want 2", got)
	}
	if got := counters.seen.Load(); got != 1 {
		t.Fatalf("seen = %d, want 1", got)
	}
	if got := counters.matched.Load(); got != 1 {
		t.Fatalf("matched = %d, want 1", got)
	}
	if got := counters.unmatched.Load(); got != 0 {
		t.Fatalf("unmatched = %d, want 0", got)
	}
}

func TestRequestMetricsCountUnmatchedFinalRequest(t *testing.T) {
	resetRequestMetricTestState()
	t.Cleanup(resetRequestMetricTestState)

	observeRequestPhase(upstreamRequest{RequestID: "request-2", Phase: "pre_cloak"}, false)
	observeRequestPhase(upstreamRequest{RequestID: "request-2", Phase: "final"}, false)

	if got := counters.seen.Load(); got != 1 {
		t.Fatalf("seen = %d, want 1", got)
	}
	if got := counters.matched.Load(); got != 0 {
		t.Fatalf("matched = %d, want 0", got)
	}
	if got := counters.unmatched.Load(); got != 1 {
		t.Fatalf("unmatched = %d, want 1", got)
	}
}

func TestRequestMetricsUpgradeRetryFromUnmatchedToMatched(t *testing.T) {
	resetRequestMetricTestState()
	t.Cleanup(resetRequestMetricTestState)

	req := upstreamRequest{RequestID: "request-retry", Phase: "final"}
	observeRequestPhase(req, false)
	observeRequestPhase(req, true)
	observeRequestPhase(req, false)

	if got := counters.seen.Load(); got != 1 {
		t.Fatalf("seen = %d, want 1", got)
	}
	if got := counters.interceptCalls.Load(); got != 3 {
		t.Fatalf("intercept calls = %d, want 3", got)
	}
	if got := counters.matched.Load(); got != 1 {
		t.Fatalf("matched = %d, want 1", got)
	}
	if got := counters.unmatched.Load(); got != 0 {
		t.Fatalf("unmatched = %d, want 0", got)
	}
}

func TestRequestMetricsCountProbeWithoutRequestIDOnlyAtFinal(t *testing.T) {
	resetRequestMetricTestState()
	t.Cleanup(resetRequestMetricTestState)

	observeRequestPhase(upstreamRequest{Phase: "pre_cloak"}, true)
	observeRequestPhase(upstreamRequest{Phase: "final"}, true)

	if got := counters.interceptCalls.Load(); got != 2 {
		t.Fatalf("intercept calls = %d, want 2", got)
	}
	if got := counters.seen.Load(); got != 1 {
		t.Fatalf("seen = %d, want 1", got)
	}
	if got := counters.matched.Load(); got != 1 {
		t.Fatalf("matched = %d, want 1", got)
	}
}

func TestRequestMetricsDeduplicateOutcomesAndCleanup(t *testing.T) {
	resetRequestMetricTestState()
	t.Cleanup(resetRequestMetricTestState)

	req := upstreamRequest{RequestID: "request-3", Phase: "final"}
	observeRequestPhase(req, true)
	if !recordRequestMetric(req, "injected") || recordRequestMetric(req, "injected") {
		t.Fatal("injected outcome was not deduplicated")
	}
	if !recordRequestMetric(req, "strict_takeover") || recordRequestMetric(req, "strict_takeover") {
		t.Fatal("strict outcome was not deduplicated")
	}
	deleteRequestMetrics(req.RequestID)
	if recordRequestMetric(req, "injected") != true {
		t.Fatal("deleted request state was not cleared")
	}
	if got := counters.seen.Load(); got != 2 {
		t.Fatalf("seen after a new request reusing the ID = %d, want 2", got)
	}
}
