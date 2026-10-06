package mcptools_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStepTimeout_SaysStallDetectionNeedsSteps: nothing LastPing sets up for an
// agent sends /step, so step_timeout_s says so in the hosted server's words,
// or an agent arms it on a hook-reported monitor and is paged on every run.
// The sentences are written out here rather than read from the constant, so
// this test cannot pass by comparing the source to itself.
func TestStepTimeout_SaysStallDetectionNeedsSteps(t *testing.T) {
	for _, tool := range []string{"create_monitor", "update_monitor"} {
		desc := paramDescription(t, tool, "step_timeout_s")
		assert.Contains(t, desc, "The Claude Code hook and the reporting prompt send no steps.", tool)
		assert.Contains(t, desc, "When set, a job that sends no steps opens a stalled incident on every run", tool)
	}
}

// TestUpstreamCause_IsNamedWhereCausesAre: the hosted server opens an
// 'upstream' incident when runs in a row end on the model provider's API
// error. Every place that lists incident causes has to name it.
func TestUpstreamCause_IsNamedWhereCausesAre(t *testing.T) {
	assert.Contains(t, paramDescription(t, "set_alert_template", "cause"), "'upstream'")
	assert.Contains(t, paramDescription(t, "create_monitor", "failure_threshold"), "'upstream' incident")
	assert.Contains(t, toolDescription(t, "list_open_incidents"), "'upstream' (consecutive model-provider API errors")
	assert.Contains(t, toolDescription(t, "list_runs"), "failure_cause is 'upstream'")
}

// TestAssertionCause_IsNamedInListRuns: the hosted server marks a run that
// sent a success but failed an output assertion as failed, with failure_cause
// 'assertion' and no upstream_error. list_runs has to say so in its words.
func TestAssertionCause_IsNamedInListRuns(t *testing.T) {
	assert.Contains(t, toolDescription(t, "list_runs"), "'assertion' when a success failed an output assertion or declared expectation")
}

// TestTraceDiagnostics_NamesTheDroppedOutcome: routine 202 drops now read
// outcome dropped, not refused, and the description must tell them apart.
func TestTraceDiagnostics_NamesTheDroppedOutcome(t *testing.T) {
	desc := toolDescription(t, "get_trace_diagnostics")
	assert.Contains(t, desc, "outcome (accepted; refused: something to fix; dropped: routine, answered 202)")
	assert.Contains(t, desc, "Dropped: unknown_event, unknown_metric, cumulative_temporality, invalid_point")
}
