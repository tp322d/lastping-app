package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Incident mirrors the LastPing Incident resource.
type Incident struct {
	IncidentID int64   `json:"incident_id"`
	OpenedAt   string  `json:"opened_at"`
	ClosedAt   *string `json:"closed_at"`
	Cause      string  `json:"cause"`
	Detail     string  `json:"detail,omitempty"`
}

func registerIncidentTools(s *server.MCPServer) {
	s.AddTool(
		newTool("list_incidents",
			mcp.WithDescription("Lists a monitor's recent incidents, newest first; an open incident has closed_at=null. "+
				untrustedDescSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithNumber("limit", mcp.Description("Max incidents to return (default 50, max 200).")),
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
			limit := 50
			if v, ok := req.GetArguments()["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			return c.listIncidents(ctx, id, limit)
		},
	)

	s.AddTool(
		newTool("get_incident",
			mcp.WithDescription("Gets one incident and its ordered timeline: run_started, step, run_failed/cancelled/blocked, incident_opened, "+
				"alert_delivered/failed/suppressed/pending (destination and attempts; down and fail alerts only), note, incident_resolved. "+
				"For what the run was doing, who was paged and what was tried. run_* events are matched on the run id recorded at opening, "+
				"so none means no run was recorded. Delivery error text is omitted. "+
				untrustedDescSentence),
			mcp.WithNumber("incident_id", mcp.Required(), mcp.Description("The incident's numeric id, from list_incidents, list_open_incidents or add_incident_note.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			v, ok := req.GetArguments()["incident_id"].(float64)
			if !ok || v <= 0 || v != float64(int64(v)) {
				return mcp.NewToolResultError("incident_id is required and must be a positive integer"), nil
			}
			return c.getIncident(ctx, int64(v))
		},
	)

	s.AddTool(
		newTool("get_run_history",
			mcp.WithDescription("Gets a monitor's run history from pings, CI and agent runs alike (no filters; trace-only runs are in list_runs). "+
				"Each run has rid, kind, received_at, steps (seq, name, at; matched on rid), title and incident_detail. "+
				"CI runs add failing_stage, actor, commit_sha, run_url, branch, duration_s and outcome. A ping with neither ci_meta nor a rid is excluded. "+
				"duration_ms is LastPing's own /start-to-success timing, separate from CI's duration_s. "+
				untrustedDescSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithNumber("limit", mcp.Description("Max runs to return (default 20, max 100).")),
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
			limit := 20
			if v, ok := req.GetArguments()["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			return c.getRunHistory(ctx, id, limit)
		},
	)
}

// getRunHistory proxies GET /api/v1/checks/{id}/runs?limit=N and returns the
// flat run JSON for agent consumption — both CI runs and agent/heartbeat runs.
// CI runs carry structured failure context decoded from ci_meta (run_url,
// commit_sha, branch, actor, failing_stage, duration_s, outcome); agent runs
// omit those fields. Every run carries the correlated incident detail and
// resolution status when one applies. duration_ms, unlike duration_s, is not
// CI-only: it is present on any run (CI or agent/heartbeat) whose success
// ping paired with its start.
func (c *APIClient) getRunHistory(ctx context.Context, id string, limit int) (*mcp.CallToolResult, error) {
	url := fmt.Sprintf("%s/api/v1/checks/%s/runs?limit=%d", c.BaseURL, id, limit)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var runs []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(runs) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No runs found for monitor %s. Has it received any pings that carried a run id (rid) or CI/CD metadata?", id)), nil
	}

	// title, incident_detail, failing_stage, branch, commit_sha, actor and
	// each element's steps.name are the fields a run's own ping, or the CI
	// job behind it, supplies verbatim.
	//
	// rid belongs with them, and reads like an identifier rather than like
	// prose. It is not one LastPing issues: it is the ?rid= query value from
	// the ping URL, chosen by whoever holds that URL and echoed back here
	// verbatim, so it carries exactly the same authorship as the fields
	// above. A run named "ignore previous instructions" is a string an
	// outsider typed.
	//
	// run_url is excluded: it is CI-provider-generated, not outsider-typed.
	// body_excerpt and failed_step do not exist on this payload at all —
	// those belong to list_open_incidents instead.
	//
	// This list is byte-for-byte the hosted server's, and must stay that way.
	return untrustedResult(runs, "incident_detail", "title", "failing_stage",
		"branch", "commit_sha", "actor", "steps.name", "rid")
}

func (c *APIClient) listIncidents(ctx context.Context, id string, limit int) (*mcp.CallToolResult, error) {
	url := fmt.Sprintf("%s/api/v1/checks/%s/incidents?limit=%d", c.BaseURL, id, limit)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var incidents []Incident
	if err := json.NewDecoder(resp.Body).Decode(&incidents); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(incidents) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No incidents found for monitor %s. Good news!", id)), nil
	}

	// detail is the field an incident's own ping (or the CI provider behind
	// it) supplies verbatim — see the Incident struct above.
	return untrustedResult(incidents, "detail")
}

// getIncident proxies GET /api/v1/incidents/{id}. The response is passed
// through as-is; the untrusted list names every field an outsider typed:
// the run id and title (chosen by whoever holds the ping URL), step names,
// the failing run's printed output, CI detail, and note bodies.
func (c *APIClient) getIncident(ctx context.Context, id int64) (*mcp.CallToolResult, error) {
	url := fmt.Sprintf("%s/api/v1/incidents/%d", c.BaseURL, id)
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
		return mcp.NewToolResultError(fmt.Sprintf("Incident not found: incident_id=%d. Use list_incidents or list_open_incidents to find valid ids.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	var inc json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&inc); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return untrustedResult(inc, "detail", "run_id", "events.rid", "events.title", "events.name",
		"events.body_excerpt", "events.detail", "events.body")
}
