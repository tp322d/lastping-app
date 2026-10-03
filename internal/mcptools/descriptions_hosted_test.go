package mcptools_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStepTimeout_SaysStallDetectionNeedsSteps: nothing LastPing sets up for an
// agent sends /step, so step_timeout_s must say so in the hosted server's
// words, or an agent arms it on a hook-reported monitor and is paged on every
// run. The sentence is written out here rather than read from the constant, so
// this test cannot pass by comparing the source to itself.
func TestStepTimeout_SaysStallDetectionNeedsSteps(t *testing.T) {
	const want = "Stall detection needs your job to call /step: the Claude Code hook and the reporting prompt do not send steps, so leave step_timeout_s unset for them."
	for _, tool := range []string{"create_monitor", "update_monitor"} {
		assert.Contains(t, paramDescription(t, tool, "step_timeout_s"), want, tool)
	}
}

// TestUpstreamCause_IsNamedWhereCausesAre: the hosted server opens an
// 'upstream' incident when runs in a row end on the model provider's API
// error. Every place that lists incident causes has to name it.
func TestUpstreamCause_IsNamedWhereCausesAre(t *testing.T) {
	assert.Contains(t, paramDescription(t, "set_alert_template", "cause"), "'upstream'")
	assert.Contains(t, paramDescription(t, "create_monitor", "failure_threshold"), "'upstream' incident")
	assert.Contains(t, toolDescription(t, "list_open_incidents"), "'upstream' means")
	assert.Contains(t, toolDescription(t, "list_runs"), "failure_cause is 'upstream'")
}

// TestAssertionCause_IsNamedInListRuns: the hosted server marks a run that
// sent a success but failed an output assertion as failed, with failure_cause
// 'assertion' and no upstream_error. list_runs has to say so in its words.
func TestAssertionCause_IsNamedInListRuns(t *testing.T) {
	assert.Contains(t, toolDescription(t, "list_runs"), "failure_cause is 'assertion' when the run sent a success but its output failed an output assertion")
}

// TestTraceDiagnostics_NamesTheDroppedOutcome: routine 202 drops now read
// outcome dropped, not refused, and the description must tell them apart.
func TestTraceDiagnostics_NamesTheDroppedOutcome(t *testing.T) {
	desc := toolDescription(t, "get_trace_diagnostics")
	assert.Contains(t, desc, "outcome is accepted, refused")
	assert.Contains(t, desc, "outcome dropped: unknown_event, unknown_metric, cumulative_temporality and invalid_point")
}
