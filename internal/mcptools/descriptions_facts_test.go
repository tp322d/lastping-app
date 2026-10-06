package mcptools_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The tests in this file pin the facts each tool definition still has to
// carry after the descriptions were rewritten to state facts about the tool
// rather than instructions to the model (policy_test.go guards the wording).
// Each one names the phrase an agent relies on, so a shortening that drops
// the fact fails here instead of passing the policy guard silently. The
// phrases match the hosted server at mcp.lastping.dev.

// TestDiscoverMonitorsReconcile_DescribesTheStakes: the call creates monitors
// that can page someone, and nothing it creates can be deleted through it.
func TestDiscoverMonitorsReconcile_DescribesTheStakes(t *testing.T) {
	desc, _ := toolSurface(t, "discover_monitors_reconcile")
	assert.NotContains(t, desc, "PROPOSE")
	assert.Contains(t, desc, "Creates one monitor per source not already monitored")
	assert.Contains(t, desc, "Each created monitor can page a person; this endpoint has no delete path")
}

// TestDiscoverMonitorsReconcile_DescribesReadingTheZone: which kinds fire in
// host-local time, the command that reads it, and why a guessed UTC is
// undetectable.
func TestDiscoverMonitorsReconcile_DescribesReadingTheZone(t *testing.T) {
	desc, _ := toolSurface(t, "discover_monitors_reconcile")
	assert.Contains(t, desc, "crontab and systemd-timer fire in the host's local time")
	assert.Contains(t, desc, "github-actions and k8s-cronjob in UTC")
	assert.Contains(t, desc, "timedatectl show -p Timezone --value")
	assert.Contains(t, desc, "a changed ref creates a duplicate")
	assert.Contains(t, desc, "the API cannot tell a guessed 'UTC' from one read from the host")
	assert.Contains(t, desc, "a crontab ref names the file and the command, so each line is its own source")

	sourcesDesc := paramDescription(t, "discover_monitors_reconcile", "sources")
	assert.Contains(t, sourcesDesc, "required on a crontab or systemd-timer entry with schedule_cron")
	assert.Contains(t, sourcesDesc, `"source_ref":"/etc/cron.d/backup:/usr/local/bin/backup.sh"`)
	assert.Contains(t, sourcesDesc, "a repeated source_kind/source_ref pair is rejected")
}

// TestDiscoverMonitorsReconcile_DescribesNoDeletes: the untouched-existing
// guarantee and the payoff for re-running.
func TestDiscoverMonitorsReconcile_DescribesNoDeletes(t *testing.T) {
	desc, _ := toolSurface(t, "discover_monitors_reconcile")
	assert.Contains(t, desc, "never deletes, pauses or edits existing monitors")
	assert.Contains(t, desc, "`existing` (unmodified, hand-tuned values intact)")
	assert.Contains(t, desc, "a scheduled re-run is safe drift detection")
	for _, name := range []string{"`created`", "`existing`", "`orphaned`"} {
		assert.Contains(t, desc, name)
	}
}

// TestDiscoverMonitorsReconcile_DescribesTheEmptyScan: a partial payload is
// not a partial update, and the caps are 400s an agent cannot anticipate.
func TestDiscoverMonitorsReconcile_DescribesTheEmptyScan(t *testing.T) {
	sourcesDesc := paramDescription(t, "discover_monitors_reconcile", "sources")
	assert.Contains(t, sourcesDesc, "'[]' orphans every discovered monitor, deleting none")
	assert.Contains(t, sourcesDesc, "1000")
	assert.Contains(t, sourcesDesc, "100-monitor cap")
	assert.Contains(t, sourcesDesc, "An omitted source is reported orphaned")
}

// TestAgentTools_DescribeMergePatchAndSurvivingMonitors: update_agent's
// description argument says how to keep and clear it, and delete_agent says
// its monitors survive and how.
func TestAgentTools_DescribeMergePatchAndSurvivingMonitors(t *testing.T) {
	d := paramDescription(t, "update_agent", "description")
	assert.Contains(t, d, "Omitted: unchanged")
	assert.Contains(t, d, "empty string")

	desc, _ := toolSurface(t, "delete_agent")
	assert.Contains(t, desc, "Its monitors are not deleted")
	assert.Contains(t, desc, "(agent_id null)")
	for _, want := range []string{"ping history", "incidents", "unowned"} {
		assert.Contains(t, desc, want)
	}
}

// TestUpdateMonitor_AssertionsAndGuardsDescribeReplaceSemantics: both
// sub-resource arguments replace the whole set, and say how to keep and clear
// them.
func TestUpdateMonitor_AssertionsAndGuardsDescribeReplaceSemantics(t *testing.T) {
	for _, param := range []string{"assertions", "guards"} {
		desc := paramDescription(t, "update_monitor", param)
		assert.Contains(t, desc, "The array replaces the whole set", param)
		assert.Contains(t, desc, "omitted leaves it", param)
		assert.Contains(t, desc, "'[]'", param)
	}
	desc := paramDescription(t, "update_monitor", "assertions")
	for _, kind := range []string{"contains", "not_contains", "matches", "json_path"} {
		assert.Regexp(t, `\b`+kind+`\b`, desc, "kind %q must be named", kind)
	}
	for _, op := range []string{"eq", "ne", "gt", "gte", "lt", "lte"} {
		assert.Regexp(t, `\b`+op+`\b`, desc, "op %q must be named", op)
	}
	assert.Contains(t, desc, "otherwise compared as strings")

	guards := paramDescription(t, "update_monitor", "guards")
	assert.Contains(t, guards, "At most 5 guards, window_s at most 604800 (7 days)")
	assert.Contains(t, guards, "skipped, not counted as zero")

	// This binary does not validate entries itself: the API refuses a
	// malformed one after update_monitor's other fields may already be saved,
	// so the descriptions claim only that the set is left as it was.
	assert.Contains(t, desc, "a malformed entry is rejected by name; the set stays as is")
	assert.Contains(t, guards, "a malformed entry is rejected by name; the set stays as is")
	assert.NotContains(t, desc, "nothing is written")
	assert.NotContains(t, guards, "nothing is written")

	updateDesc, _ := toolSurface(t, "update_monitor")
	assert.Contains(t, updateDesc, "assertions (conditions a successful run's ping body has to satisfy")
}

// TestSetRoute_DescribesTheRateBudgetsAndTheBlockedHold.
func TestSetRoute_DescribesTheRateBudgetsAndTheBlockedHold(t *testing.T) {
	desc := paramDescription(t, "set_route", "event_type")
	assert.Contains(t, desc, "in_progress")
	assert.Contains(t, desc, "share a separate per-channel rate cap")
	assert.Contains(t, desc, "GitHub's requested and in_progress each send one, so one CI run can notify twice")
	assert.Contains(t, desc, "held 10 minutes, cancelled by a ping or step of the same run in that time")
	assert.Contains(t, desc, "blocked draws on the protected down/fail/recovery budget")
}

// TestFailureInboxTools_StateTheirFacts: absence is no evidence, notes are
// append-only and ordinary, and an unfixed failure is worth a note too.
func TestFailureInboxTools_StateTheirFacts(t *testing.T) {
	desc, _ := toolSurface(t, "list_open_incidents")
	assert.Contains(t, strings.ToLower(desc), "no evidence")
	assert.Contains(t, desc, "normal")
	assert.Contains(t, desc, "clean exit")

	note, _ := toolSurface(t, "add_incident_note")
	assert.NotContains(t, note, "declared ONCE")
	assert.Contains(t, note, "Several notes per incident are normal")
	assert.Contains(t, note, "A note can record a failure that was not fixed as well as one that was")
	assert.Contains(t, note, "Without a note, the incident page shows only the alert")
	assert.Contains(t, paramDescription(t, "add_incident_note", "body"), "8192")
}

// TestDeclareRunExpectations_StatesItsFacts: a positive criterion is what
// catches an empty run, and a declaration is immutable.
func TestDeclareRunExpectations_StatesItsFacts(t *testing.T) {
	paramDesc := paramDescription(t, "declare_run_expectations", "assertions")
	assert.Contains(t, paramDesc, "a contains, json_path or empty-rejecting matches entry catches a run that produced nothing")
	assert.Contains(t, paramDesc, "not_contains")
	assert.Contains(t, paramDesc, "pass on empty output")
	assert.Contains(t, paramDesc, "a matches pattern that accepts an empty body")
	assert.Contains(t, paramDesc, "'.*'")

	toolDesc, _ := toolSurface(t, "declare_run_expectations")
	assert.Contains(t, toolDesc, "at the start of a run")
	assert.Contains(t, toolDesc, "so the run does not grade itself")
	assert.Contains(t, toolDesc, "One declaration per rid, immutable (a second call is a conflict)")
}

// TestBlockedTimeout_DescribesTheFallbackPlainly: unset is the 24-hour
// default, not an unbounded wait.
func TestBlockedTimeout_DescribesTheFallbackPlainly(t *testing.T) {
	for _, tool := range []string{"create_monitor", "update_monitor"} {
		desc := paramDescription(t, tool, "blocked_timeout_s")
		assert.Contains(t, desc, "Unset means the 24-hour default, not no limit", tool)
		assert.NotContains(t, desc, "wait forever", tool)
	}
}

// TestGetPingInstructions_DescriptionSaysHowToolSelectsTheInstall.
func TestGetPingInstructions_DescriptionSaysHowToolSelectsTheInstall(t *testing.T) {
	desc, _ := toolSurface(t, "get_ping_instructions")
	assert.Contains(t, desc, "returned only when `tool` is set (claude-code, codex or antigravity)")
	assert.NotContains(t, desc, "two hooks")
	assert.NotContains(t, desc, "discovery_how_to")
}

// TestToolDefinitions_KeepRestoredFacts pins facts a shortening could drop,
// each checked against the behaviour it describes.
func TestToolDefinitions_KeepRestoredFacts(t *testing.T) {
	for _, c := range []struct{ tool, phrase string }{
		{"get_run_history", "A ping with neither ci_meta nor a rid is excluded"},
		{"get_run_history", "actor, commit_sha, run_url"},
		{"list_runs", "is_test (excluded from counts)"},
		{"get_agent_dependencies", "sampled from the ten newest traced runs"},
		{"create_api_key", "create_ingest_key, which needs only the write scope, mints a single-monitor tracing key"},
	} {
		desc, _ := toolSurface(t, c.tool)
		assert.Contains(t, desc, c.phrase, c.tool)
	}
}
