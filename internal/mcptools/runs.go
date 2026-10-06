package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerRunTools registers get_run: one run's full timeline, its declared
// expectations judged against its terminal ping body, its output excerpt
// and, when it is a CI run, its provider metadata.
//
// It is a separate file from get_run_history (incidents.go) because the two
// answer different questions: get_run_history is the list an agent scans to
// find which run to look at; get_run is what it calls once it has picked
// one, for the full per-event timeline and assertion verdicts the list
// endpoint does not carry.
func registerRunTools(s *server.MCPServer) {
	registerListRunsTool(s)
	s.AddTool(
		newTool("get_run",
			mcp.WithDescription("Gets one run's full timeline: every recorded event (start, step, log, success/fail/cancel, incident_opened) in time order, "+
				"its declared assertions with pass/fail/not_evaluated verdicts against the terminal ping body, the terminal output excerpt, "+
				"CI metadata when present, and its OTLP spans (spans[], parents before children, siblings by start time) when traced. "+
				"A current Claude Code hook's run holds its turns' traces; a prompt after an interrupt or a blocked turn continues the open run. "+
				"Also carries project, receiving_spans, in_hook_session, is_test, failure_cause and upstream_error (as in list_runs). "+
				"For the detail behind a run (id + rid) found elsewhere. "+
				"outcome: succeeded, failed, cancelled, blocked, running or unfinished (no end ping, no incident, not blocked, older than max_runtime_s or 24h; never pages). "+
				"Caps: 200 events (events_truncated; the terminal event is kept), 2,000 spans (spans_truncated, spans_dropped). "+
				"A span's gen_ai block (system, model, tokens, cost_usd) is present only on GenAI calls. "+
				untrustedDescSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("rid", mcp.Required(), mcp.Description("The run id as sent on the ping.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rid, err := req.RequireString("rid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getRun(ctx, id, rid)
		},
	)
}

// runOutcomes is every value list_runs' outcome filter accepts, in the API's
// order.
var runOutcomes = []string{"succeeded", "failed", "cancelled", "blocked", "running", "unfinished"}

// listRunsTextParams are list_runs' string filters, forwarded under the
// query name the API gives them (GET /api/v1/runs).
var listRunsTextParams = []string{"monitor", "outcome", "since", "until", "project", "agent", "dependency", "operation", "model",
	"min_cost_usd", "trace_id", "q", "cursor"}

// registerListRunsTool registers list_runs, the proxy for GET /api/v1/runs:
// every run across every monitor of the project, newest first, with the
// Traced view's filters.
//
// It is a separate tool from get_run_history rather than an extension of it:
// get_run_history proxies the ping-based
// GET /api/v1/checks/{id}/runs, which takes only limit and cannot list a run
// that exists only as spans, and widening it would give one tool two response
// shapes.
func registerListRunsTool(s *server.MCPServer) {
	s.AddTool(
		newTool("list_runs",
			mcp.WithDescription("Lists runs across every monitor in the project, newest started first, including runs that exist only as OpenTelemetry traces (traced: true). "+
				"Each run carries check_id, check_name, rid, title, outcome, started_at, ended_at, duration_ms, step_count, exit_code, its incident, "+
				"and when traced span_count, tokens, cost_usd and cost_source (client, estimated or mixed); plus agent_id, agent_name, source_name, "+
				"multi_trace, project (the Claude Code session's folder), receiving_spans (spans in the last 2 minutes), in_hook_session, is_test (excluded from counts). "+
				"failure_cause is 'upstream' when the run ended on the model provider's API error (pages after 3 in a row, or failure_threshold when higher; "+
				"upstream_error names it) or 'assertion' when a success failed an output assertion or declared expectation. "+
				"outcome: succeeded, failed, cancelled, blocked, running or unfinished (started, never ended within max_runtime_s or 24h; never pages). "+
				"Filters combine and also narrow counts (per-outcome totals). Pages with next_cursor. "+
				untrustedDescSentence),
			mcp.WithString("monitor", mcp.Description("Only this monitor's runs (monitor UUID).")),
			mcp.WithString("outcome", mcp.Enum(runOutcomes...), mcp.Description("Only runs with this outcome.")),
			mcp.WithString("since", mcp.Description("RFC 3339 start of the window, e.g. 2026-09-01T00:00:00Z. Default 7 days ago; at most 90 days back.")),
			mcp.WithString("until", mcp.Description("RFC 3339 end of the window. Default now.")),
			mcp.WithString("project", mcp.Description("Only runs whose Claude Code hook start carried this project label: the name of the folder the session worked in.")),
			mcp.WithBoolean("traced", mcp.Description("true: only runs that hold spans. Omitted or false: every run.")),
			mcp.WithString("agent", mcp.Description("Only runs of this agent (agent UUID or slug).")),
			mcp.WithString("dependency", mcp.Description("Only runs that called this dependency, by its exact name as get_agent_dependencies "+
				"reports it (e.g. api.github.com).")),
			mcp.WithString("operation", mcp.Description("Only runs with a span whose name starts with this text.")),
			mcp.WithString("model", mcp.Description("Only runs that called this model, by exact model id.")),
			mcp.WithBoolean("has_error", mcp.Description("true: only runs with a span that reported an error. false: only runs without one.")),
			mcp.WithNumber("min_duration_ms", mcp.Description("Only runs that took at least this many milliseconds.")),
			mcp.WithString("min_cost_usd", mcp.Description("Only runs whose cost is at least this many US dollars, as a decimal "+
				"string, e.g. \"0.25\".")),
			mcp.WithString("trace_id", mcp.Description("Only the run a trace became: 8 to 32 hex digits of its trace id. Finds runs made "+
				"from a trace with no run id, not runs that named their own.")),
			mcp.WithString("q", mcp.Description("Only runs whose run id (rid) contains this text.")),
			mcp.WithNumber("limit", mcp.Description("Runs per page (default 20, max 100).")),
			mcp.WithString("cursor", mcp.Description("next_cursor from the previous page, verbatim.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			args := req.GetArguments()
			q := url.Values{}
			for _, name := range listRunsTextParams {
				if v, ok := args[name].(string); ok && v != "" {
					q.Set(name, v)
				}
			}
			// Booleans are forwarded on PRESENCE: has_error=false is a
			// filter of its own (runs with no error span), not an omission.
			for _, name := range []string{"traced", "has_error"} {
				if v, ok := args[name].(bool); ok {
					q.Set(name, strconv.FormatBool(v))
				}
			}
			if v, ok := args["min_duration_ms"].(float64); ok && v >= 0 {
				q.Set("min_duration_ms", strconv.FormatInt(int64(v), 10))
			}
			if v, ok := args["limit"].(float64); ok && v > 0 {
				q.Set("limit", strconv.FormatInt(int64(v), 10))
			}
			// title and rid are what the run's own ping chose (as in
			// get_run_history); source_name is the exporter's service.name;
			// agent_name can be that same service.name when the agent was
			// created by adopting a discovered source. project is the label
			// the Claude Code hook sent on the run's start (?project=, the
			// working directory's basename): whoever holds the ping URL
			// chooses it, and its sanitising still lets a sentence through.
			// check_name is a monitor name a project member gave, and
			// incident.cause, outcome and failure_cause are words the server
			// picks. upstream_error is one of three server words too
			// (server_error, overloaded, rate_limit: the server reports the
			// stored error type only when it is one of those, and an empty
			// string otherwise); it stays listed because the value is copied
			// from the hook's ?error_type=, so the list holds if that rule
			// ever widens.
			return c.proxyRead(ctx, "/api/v1/runs", q, "",
				"runs.title", "runs.rid", "runs.source_name", "runs.agent_name", "runs.project", "runs.upstream_error")
		},
	)
}

// getRun proxies GET /api/v1/checks/{id}/runs/{rid} and returns the run
// detail JSON for agent consumption: the same summary fields as a
// get_run_history entry, plus the full events[] timeline, steps[],
// assertions[] verdicts, output_excerpt, ci, events_truncated and
// spans[]/spans_truncated/spans_dropped.
func (c *APIClient) getRun(ctx context.Context, id, rid string) (*mcp.CallToolResult, error) {
	url := fmt.Sprintf("%s/api/v1/checks/%s/runs/%s", c.BaseURL, id, rid)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return mcp.NewToolResultError(fmt.Sprintf(
			"run %s not found on monitor %s (runs older than 90 days are pruned)", rid, id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var detail json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	// title, output_excerpt, rid, events.body, events.label, steps.name and
	// assertions.failure are the fields the run detail payload carries
	// verbatim from the run's own ping, or the CI job behind it — the same
	// authorship test get_run_history and get_incident apply. events.label is
	// a step's own name for a `step` event and an incident's cause for
	// `incident_opened`, both outsider-chosen text; it duplicates steps.name
	// for step events but is listed separately because it lives at a
	// different JSON path. assertions.failure carries the evaluator's reason
	// string, built from the terminal ping body an outsider supplied.
	// run_url, commit_sha, branch, actor and failing_stage are provider/CI
	// fields already excluded from get_run_history's own set on the same
	// reasoning and stay excluded here.
	//
	// spans.name, spans.status_message and spans.attributes are span content
	// an exporter chose to send — the same authorship as a ping's
	// body_excerpt, just arriving over OTLP instead of a ping URL.
	// spans.gen_ai.model and spans.gen_ai.system are also caller-chosen
	// strings (a model name or SDK's own self-reported name), unlike
	// spans.gen_ai.tokens_in/tokens_out/cost_usd, which are numbers derived
	// by the SDK/provider and carry no injectable text.
	//
	// source_name and spans.source_name are the exporter's own service.name,
	// and spans.peer_name is derived from span attributes the exporter chose
	// (a host, a database, a model id): outsider text on the same reasoning.
	// spans.peer_kind is one of seven words the server picks, so it is not
	// listed. agent_name is too: adopting a discovered source with no body
	// names the new agent after the source's service.name (POST
	// /api/v1/agents/discovered/{id}/adopt), so an agent's name can be the
	// exporter's string verbatim.
	//
	// project is the Claude Code hook's label for the run's start
	// (?project=), chosen by whoever holds the ping URL, the same authorship
	// as title and rid. upstream_error is one of three server words
	// (server_error, overloaded, rate_limit: the server reports the fail
	// ping's error type only when it is one of those), copied from the
	// hook's ?error_type=, so it stays listed in case that rule ever widens;
	// failure_cause is a server word.
	//
	// This list is byte-for-byte the hosted server's, and must stay that way.
	return untrustedResult(detail, "title", "output_excerpt", "rid",
		"events.body", "events.label", "steps.name", "assertions.failure",
		"spans.name", "spans.status_message", "spans.attributes",
		"spans.gen_ai.model", "spans.gen_ai.system",
		"source_name", "spans.source_name", "spans.peer_name", "agent_name", "project", "upstream_error")
}
