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
	OpenedAt string  `json:"opened_at"`
	ClosedAt *string `json:"closed_at"`
	Cause    string  `json:"cause"`
	Detail   string  `json:"detail,omitempty"`
}

func registerIncidentTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("list_incidents",
			mcp.WithDescription("List recent incidents (downtime events) for a monitor. Returns newest first. An open incident has closed_at=null."),
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
		mcp.NewTool("get_run_history",
			mcp.WithDescription("Get structured run history for a monitor — both CI/CD runs and agent/heartbeat runs. "+
				"Each run carries its run id (rid), kind, received_at, the progress steps reported under it "+
				"(steps: seq, name, at), and the correlated incident log excerpt (incident_detail) with resolution status. "+
				"A run that stalled tells you which step it reached and when it stopped moving — no need to follow "+
				"links to the CI provider. steps is absent for a run that reported none — steps are matched on rid, "+
				"so they appear only when the job or agent posted /step?rid= with the same run id it started with. "+
				"CI-specific fields — failing step (failing_stage), triggering actor, commit SHA, run URL, branch, "+
				"duration_s, outcome — are present only on runs that carried ci_meta; they are simply absent on "+
				"agent/heartbeat runs. A ping with neither ci_meta nor a rid is excluded entirely. "+
				"duration_ms is a SEPARATE measurement, present on ANY run (CI or agent/heartbeat) whose success "+
				"ping paired with its preceding start — this is how to answer 'how long does this job normally "+
				"take?' for a non-CI monitor. It is computed by LastPing from the /start->success timing, not "+
				"self-reported by a provider like duration_s is; the two must not be confused as confirming "+
				"each other, and either can be present without the other."),
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

	out, _ := json.MarshalIndent(runs, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
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

	out, _ := json.MarshalIndent(incidents, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}
