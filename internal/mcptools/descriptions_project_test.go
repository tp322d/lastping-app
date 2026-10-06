package mcptools_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListRuns_ForwardsProject: the project filter reaches GET /api/v1/runs's
// query string under the name the API reads, and nothing else rides along.
func TestListRuns_ForwardsProject(t *testing.T) {
	st, c := newStub(t, http.StatusOK, `{"runs":[],"next_cursor":"","counts":{"total":0}}`)
	res := callTool(t, obsServer(t), c, "list_runs", map[string]interface{}{"project": "billing-api"})
	require.False(t, res.IsError, extractText(res))
	call := st.only(t)
	assert.Equal(t, "GET /api/v1/runs", call.Method+" "+call.Path)
	assert.Equal(t, url.Values{"project": {"billing-api"}}, call.Query)
}

// TestGetAgentUsage_ForwardsProject: with and without an agent id, project
// reaches the usage route's query string beside range.
func TestGetAgentUsage_ForwardsProject(t *testing.T) {
	st, c := newStub(t, http.StatusOK, `{"range":"7d","days":[],"by_agent":[],"by_project":[]}`)
	res := callTool(t, obsServer(t), c, "get_agent_usage", map[string]interface{}{"id": "a1", "range": "30d", "project": "billing-api"})
	require.False(t, res.IsError, extractText(res))
	call := st.only(t)
	assert.Equal(t, "/api/v1/agents/a1/usage", call.Path)
	assert.Equal(t, url.Values{"range": {"30d"}, "project": {"billing-api"}}, call.Query)

	st.reset()
	res = callTool(t, obsServer(t), c, "get_agent_usage", map[string]interface{}{"project": "billing-api"})
	require.False(t, res.IsError, extractText(res))
	call = st.only(t)
	assert.Equal(t, "/api/v1/agents/usage", call.Path)
	assert.Equal(t, url.Values{"project": {"billing-api"}}, call.Query)
}

// TestRunDescriptions_NameTheRunFields: get_run and list_runs tell an agent
// about the fields a run now carries, and that a hooked Claude Code run holds
// its turns' traces.
func TestRunDescriptions_NameTheRunFields(t *testing.T) {
	for _, tool := range []string{"get_run", "list_runs"} {
		desc := toolDescription(t, tool)
		for _, want := range []string{
			"project",
			"receiving_spans",
			"in_hook_session",
			"is_test",
		} {
			assert.Contains(t, desc, want, tool)
		}
	}
	runs := toolDescription(t, "list_runs")
	assert.Contains(t, runs, "project (the Claude Code session's folder)")
	assert.Contains(t, runs, "receiving_spans (spans in the last 2 minutes)")
	assert.Contains(t, runs, "is_test (excluded from counts)")
	run := toolDescription(t, "get_run")
	assert.Contains(t, run, "A current Claude Code hook's run holds its turns' traces")
	assert.Contains(t, run, "a prompt after an interrupt or a blocked turn continues the open run")
	assert.Contains(t, paramDescription(t, "list_runs", "project"), "Claude Code hook start carried this project label")
}

// TestAgentUsage_DescribesByProject: by_project, the project filter, and why
// by_project and days need not agree are all in the description.
func TestAgentUsage_DescribesByProject(t *testing.T) {
	desc := toolDescription(t, "get_agent_usage")
	assert.Contains(t, desc, "Both carry by_project: per project")
	assert.Contains(t, desc, "so the two need not match")
	assert.Contains(t, desc, "With project, days become one row per UTC day with model and provider empty")
	assert.Contains(t, desc, "project narrows days and by_project, not by_agent")
	assert.Contains(t, paramDescription(t, "get_agent_usage", "project"), "metrics-only usage has no project and is left out")
}

// TestCostSource_NamesMixed: every tool that returns cost_source names all
// three values, mixed included.
func TestCostSource_NamesMixed(t *testing.T) {
	assert.Contains(t, toolDescription(t, "list_runs"), "cost_source (client, estimated or mixed")
	assert.Contains(t, toolDescription(t, "get_agent_usage"), "mixed (a traced day holds both)")
	assert.Contains(t, toolDescription(t, "get_agent_dependencies"), "cost_source (client, estimated or mixed)")
}

// TestMinCostUSD_SaysCost: the filter matches a run's cost whatever its
// source, so it must not claim to read only an estimate.
func TestMinCostUSD_SaysCost(t *testing.T) {
	desc := paramDescription(t, "list_runs", "min_cost_usd")
	assert.Contains(t, desc, "Only runs whose cost is at least")
	assert.NotContains(t, desc, "estimated cost")
}

// TestBlockedHold_IsDescribed: the 'blocked' route event is held 10 minutes
// and cancelled by further activity of the same run, in both places that
// describe it.
func TestBlockedHold_IsDescribed(t *testing.T) {
	assert.Contains(t, paramDescription(t, "set_route", "event_type"), "held 10 minutes, cancelled by a ping or step of the same run in that time")
	for _, tool := range []string{"create_monitor", "update_monitor"} {
		assert.Contains(t, paramDescription(t, tool, "blocked_timeout_s"), "held 10 minutes, sent at most once per blocked stretch of a run", tool)
	}
}

// TestOpenIncidents_DescribesTheYoungMonitorNorm: days_sampled 0 means recent
// runs on a young monitor, not missing evidence.
func TestOpenIncidents_DescribesTheYoungMonitorNorm(t *testing.T) {
	desc := toolDescription(t, "list_open_incidents")
	assert.Contains(t, desc, "days_sampled (0: the norm is the median of 5+ recent runs)")
}

// TestSnooze_SaysWhatTheWindowHoldsAndWhatStillNotifies: a window holds
// deadline incidents and failing probes, and the job's own reports still
// notify. The old "will not alert" claim is gone.
func TestSnooze_SaysWhatTheWindowHoldsAndWhatStillNotifies(t *testing.T) {
	desc := toolDescription(t, "snooze_monitor")
	assert.Contains(t, desc, "deadline incidents (a missed or never-started run, an overrun, a stall, a blocked run outliving blocked_timeout_s)")
	assert.Contains(t, desc, "on an HTTP monitor, failing probes")
	assert.Contains(t, desc, "(any fail there counts as a probe's)")
	assert.Contains(t, desc, "Still notified: the job's own fail ping, the page a blocked ping queues, a runaway ping rate")
	assert.Contains(t, desc, "Takes exactly one of duration, until or clear=true.")
	assert.NotContains(t, desc, "will not alert")
}
