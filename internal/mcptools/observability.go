package mcptools

// observability.go: the agent observability reads and the discovered agent
// actions. Every tool here is a THIN REST PROXY: it names a route, forwards
// the caller's arguments as that route's query or body, and hands the
// response back inside the untrusted-output envelope. Nothing is computed
// here, so a change on the server reaches every installed client with no
// release. It matches the hosted server at mcp.lastping.dev.
//
//   get_agent_dependencies  GET  /api/v1/agents/{id}/calls
//   get_agent_usage         GET  /api/v1/agents/{id}/usage, or /api/v1/agents/usage
//   list_dependencies       GET  /api/v1/dependencies
//   list_discovered_agents  GET  /api/v1/agents/discovered
//   adopt_discovered_agent  POST /api/v1/agents/discovered/{id}/adopt
//   get_trace_diagnostics   GET  /api/v1/checks/{id}/trace-diagnostics
//
// AUTHORSHIP. Most of what these routes return was written by an exporter,
// not by LastPing: a dependency's name comes from span attributes (a host, a
// database, a model id), a model and provider from gen_ai attributes, a trace
// source's name from its service.name, a user agent from the request header.
// Whoever holds a tracing key chooses those strings, exactly as whoever holds
// a ping URL chooses a ping body, so each tool lists them in untrusted_fields.
// An agent's name belongs in the same list: adopting a discovered source
// names the new agent after that source (POST .../adopt with no body), so an
// agent name can be an exporter's string verbatim.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Dependency mirrors the REST Dependency schema: one thing an agent calls, or
// one caller of it. Field names and tags must stay identical to the API's.
type Dependency struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Direction  string          `json:"direction"`
	Calls      int64           `json:"calls"`
	Errors     int64           `json:"errors"`
	ErrorRate  float64         `json:"error_rate"`
	P50Ms      int64           `json:"p50_ms"`
	P95Ms      int64           `json:"p95_ms"`
	P95IsFloor bool            `json:"p95_is_floor"`
	TokensIn   *int64          `json:"tokens_in"`
	TokensOut  *int64          `json:"tokens_out"`
	CostUSD    *string         `json:"cost_usd"`
	CostSource string          `json:"cost_source"`
	Series     []DependencyDay `json:"series"`
}

// DependencyDay is one point of a Dependency's series: one UTC day.
type DependencyDay struct {
	Day    string `json:"day"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
}

// UsageDay mirrors the REST UsageDay schema: one model's usage on one UTC
// day. tokens_in INCLUDES cache reads.
type UsageDay struct {
	Day              string  `json:"day"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider"`
	TokensIn         int64   `json:"tokens_in"`
	TokensOut        int64   `json:"tokens_out"`
	TokensCacheRead  int64   `json:"tokens_cache_read"`
	TokensCacheWrite int64   `json:"tokens_cache_write"`
	CostUSD          *string `json:"cost_usd"`
	CostSource       string  `json:"cost_source"`
	Origin           string  `json:"origin"`
}

// dependencyRanges and dependencyKinds are the values the routes accept. The
// server validates both; listing them here puts them in the tool's schema.
var (
	dependencyRanges = []string{"24h", "7d", "30d"}
	dependencyKinds  = []string{"model", "tool", "http", "database", "queue", "rpc", "agent"}
)

const rangeParamDesc = "Time window: 24h, 7d (the default) or 30d. It covers every UTC day that overlaps it, so 24h spans two days."

const envelopeSentence = untrustedDescSentence

func registerObservabilityTools(s *server.MCPServer) {
	s.AddTool(
		newTool("get_agent_dependencies",
			mcp.WithDescription("What one agent calls, heaviest first, from its traces: each model, tool, HTTP host, database, queue, "+
				"RPC endpoint or agent: calls, errors, error_rate (0-1), p50_ms, p95_ms (bucket ceiling; p95_is_floor: over 60s), daily series, "+
				"and for a model tokens, cost_usd and cost_source (client, estimated or mixed). operations: up to five span names per outgoing row, sampled from the ten newest traced runs. "+
				"At most 50 rows; `more` counts the rest. "+
				envelopeSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID or slug (from list_agents).")),
			mcp.WithString("range", mcp.Enum(dependencyRanges...), mcp.Description(rangeParamDesc)),
			mcp.WithString("direction", mcp.Enum("out", "in", "all"),
				mcp.Description("out (the default): what this agent calls. in: who calls it. all: both.")),
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
			q := url.Values{}
			setIf(q, "range", req.GetString("range", ""))
			setIf(q, "direction", req.GetString("direction", ""))
			return c.proxyRead(ctx, "/api/v1/agents/"+url.PathEscape(id)+"/calls", q,
				fmt.Sprintf("Agent not found: id=%s. Use list_agents to find valid IDs or slugs.", id),
				"calls.name", "calls.operations.name")
		},
	)

	s.AddTool(
		newTool("get_agent_usage",
			mcp.WithDescription("Model usage, one row per model per UTC day: tokens_in (cache reads included, so tokens_cache_read is not added to it), "+
				"tokens_out, tokens_cache_read, tokens_cache_write, cost_usd (decimal text) and cost_source: client (the tool reported its cost), "+
				"estimated (LastPing priced tokens at API list prices), mixed (a traced day holds both) or empty (unknown). "+
				"origin is traces or metrics; a day and model can have one of each, never summed. With id, one agent's usage; without, the project's, "+
				"traces only, plus by_agent (costliest first). Both carry by_project: per project (a Claude Code session's folder) its runs, tokens and cost "+
				"from traced runs only (project_scope traces_only). by_project sums each run's traced totals, while one agent's days prefer a Claude Code metrics export's "+
				"reported cost, so the two need not match. With project, days become one row per UTC day with model and provider empty; project narrows days and by_project, not by_agent. "+
				envelopeSentence),
			mcp.WithString("id", mcp.Description("Agent UUID or slug (from list_agents). Omitted: every agent in the project.")),
			mcp.WithString("range", mcp.Enum(dependencyRanges...), mcp.Description(rangeParamDesc)),
			mcp.WithString("project", mcp.Description("Only usage from traced runs of this project; metrics-only usage has no project and is left out. "+
				"days then hold one row per UTC day with model and provider empty (a run's totals are not split by model); "+
				"by_agent is not narrowed.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			q := url.Values{}
			setIf(q, "range", req.GetString("range", ""))
			setIf(q, "project", req.GetString("project", ""))
			id := req.GetString("id", "")
			// by_project.project is the Claude Code hook's ?project= label,
			// chosen by whoever holds a ping URL (the same authorship as
			// list_runs' runs.project). The top-level project only echoes
			// the caller's own filter.
			if id == "" {
				return c.proxyRead(ctx, "/api/v1/agents/usage", q, "",
					"days.model", "days.provider", "by_agent.name", "by_project.project")
			}
			return c.proxyRead(ctx, "/api/v1/agents/"+url.PathEscape(id)+"/usage", q,
				fmt.Sprintf("Agent not found: id=%s. Use list_agents to find valid IDs or slugs.", id),
				"days.model", "days.provider", "by_project.project")
		},
	)

	s.AddTool(
		newTool("list_dependencies",
			mcp.WithDescription("Everything the project's agents call, across every agent, most calls first: each dependency with "+
				"the same figures as get_agent_dependencies plus agents (which agents call it, and how often). "+
				"Answers questions such as which agents call postgres or use a model. At most 50 rows; `more` counts the rest. "+
				envelopeSentence),
			mcp.WithString("range", mcp.Enum(dependencyRanges...), mcp.Description(rangeParamDesc)),
			mcp.WithString("kind", mcp.Enum(dependencyKinds...),
				mcp.Description("Only one kind of dependency: model, tool, http, database, queue, rpc or agent. Omitted: all.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			q := url.Values{}
			setIf(q, "range", req.GetString("range", ""))
			setIf(q, "kind", req.GetString("kind", ""))
			return c.proxyRead(ctx, "/api/v1/dependencies", q, "",
				"dependencies.name", "dependencies.agents.name")
		},
	)

	s.AddTool(
		newTool("list_discovered_agents",
			mcp.WithDescription("Trace sources that sent spans but match no registered agent: id, source_name (the OpenTelemetry service.name), "+
				"first and last seen, span_count, and suggested_agent_id when a registered agent's slug or name now matches. "+
				"For traces that arrive while no agent shows them; adopt_discovered_agent counts a source under an agent. At most 200, most recently seen first. "+
				envelopeSentence),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.proxyRead(ctx, "/api/v1/agents/discovered", nil, "", "discovered.source_name")
		},
	)

	s.AddTool(
		newTool("adopt_discovered_agent",
			mcp.WithDescription("Counts a discovered trace source's traces under an agent from now on. Without agent_id, a new agent named after the source is created "+
				"(409 AGENT_EXISTS when its slug is taken). With agent_id (e.g. a suggested_agent_id), "+
				"the source merges into that agent. Earlier traces stay where they are (backfilled is false). Re-adopting into the same agent changes nothing; "+
				"into a different one is 409 ALREADY_ADOPTED. "+envelopeSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Discovered source UUID (from list_discovered_agents).")),
			mcp.WithString("agent_id", mcp.Description("Optional agent UUID (from list_agents) to merge the source into. Omitted: a new agent is created.")),
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
			return c.adoptDiscoveredAgent(ctx, id, req.GetString("agent_id", ""))
		},
	)

	s.AddTool(
		newTool("get_trace_diagnostics",
			mcp.WithDescription("Why traces, metrics or logs sent to one monitor did or did not arrive: the newest 20 ingest attempts (kept 7 days), "+
				"last_accepted_at, and the newest traced run's summary. For checking a test span, or telemetry sent with nothing showing. "+
				"Each attempt: outcome (accepted; refused: something to fix; dropped: routine, answered 202), reason, span_count, bytes, protocol, user_agent, signal. "+
				"Rejections: unsupported_media_type (not http/protobuf, e.g. gRPC to the HTTP URL), body_too_large (over 1 MB), too_many_spans or too_many_records (over 500 per batch), "+
				"unknown_monitor (lastping.monitor_id missing or outside the project), expired_key (a new tracing key comes from the Connect page), "+
				"wrong_scope (not a tracing key), wrong_project, monitor_mismatch, over_budget/over_log_budget (daily, resets 00:00 UTC), rate_limited, busy, malformed. "+
				"Refused after a 202: future_start (sender clock), too_many_series. Dropped: unknown_event, unknown_metric, cumulative_temporality, invalid_point. "+
				"No row: gRPC to the gRPC port, or a missing or wrong key. "+envelopeSentence),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("monitor_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// attempts.user_agent is the sender's own User-Agent header;
			// summary.rid is the run id the trace chose (a lastping.run_id
			// attribute, or its trace id) and summary.model the gen_ai model
			// attribute it sent. reason, outcome, protocol and signal are
			// words the server picks from fixed sets.
			return c.proxyRead(ctx, "/api/v1/checks/"+url.PathEscape(id)+"/trace-diagnostics", nil,
				fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs, or create_monitor first.", id),
				"attempts.user_agent", "summary.rid", "summary.model")
		},
	)
}

// setIf adds key=value to q when value is not empty, so an omitted argument
// leaves the server's own default in charge.
func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

// proxyRead GETs path?query and returns the body verbatim inside the
// untrusted-output envelope naming fields. notFound is the error a 404
// becomes; an empty string reports the server's own problem instead.
func (c *APIClient) proxyRead(ctx context.Context, path string, query url.Values, notFound string, fields ...string) (*mcp.CallToolResult, error) {
	target := c.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && notFound != "" {
		return mcp.NewToolResultError(notFound), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return untrustedResult(body, fields...)
}

// adoptDiscoveredAgent proxies POST /api/v1/agents/discovered/{id}/adopt.
// The body is {"agent_id"} only when the caller named one: no body is the
// route's "create a new agent" form.
func (c *APIClient) adoptDiscoveredAgent(ctx context.Context, id, agentID string) (*mcp.CallToolResult, error) {
	var reqBody *bytes.Reader
	if agentID != "" {
		data, _ := json.Marshal(map[string]string{"agent_id": agentID})
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/agents/discovered/"+url.PathEscape(id)+"/adopt", reqBody)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	// The adopted agent's name and slug are the source's service.name when
	// the adopt created it; its dependencies and usage are exporter text on
	// the same reasoning as get_agent.
	return untrustedResult(body, "agent.name", "agent.slug",
		"agent.top_dependencies.name", "agent.usage_24h.model", "agent.usage_24h.provider")
}
