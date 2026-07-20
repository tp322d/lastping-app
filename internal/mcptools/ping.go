package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPingTools registers get_ping_instructions — the flagship tool that
// turns a monitor into ready-to-run check-in snippets, so a job or AI agent can
// instrument its own dead-man's-switch in a single conversation.
func registerPingTools(s *server.MCPServer, pingHost string) {
	s.AddTool(
		mcp.NewTool("get_ping_instructions",
			mcp.WithDescription("Get the ping URL and copy-paste check-in snippets for a monitor so a job or AI agent can report that it ran. "+
				"Call this right after create_monitor. Run the success ping at the END of the monitored work; if the work fails, hit the fail URL instead. "+
				"For long or possibly-hung jobs/agent runs, send the start ping first to enable overrun / never-finished detection."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors)."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getPingInstructions(ctx, id, pingHost)
		},
	)
}

// PingInstructions is the structured, agent-friendly output of get_ping_instructions.
type PingInstructions struct {
	MonitorID   string `json:"monitor_id"`
	MonitorName string `json:"monitor_name"`
	PingURL     string `json:"ping_url"`
	SuccessURL  string `json:"success_url"`
	StartURL    string `json:"start_url"`
	FailURL     string `json:"fail_url"`
	CurlSuccess string `json:"curl_success"`
	CurlStart   string `json:"curl_start"`
	CurlFail    string `json:"curl_fail"`
	CronExample string `json:"cron_example"`
	HowTo       string `json:"how_to"`
}

func (c *APIClient) getPingInstructions(ctx context.Context, id, pingHost string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs, or create_monitor first.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	// Prefer the API-provided ping_url; fall back to constructing it.
	base := ch.PingURL
	if base == "" {
		base = pingHost + "/" + ch.ID
	}

	pi := PingInstructions{
		MonitorID:   ch.ID,
		MonitorName: ch.Name,
		PingURL:     base,
		SuccessURL:  base,
		StartURL:    base + "/start",
		FailURL:     base + "/fail",
		CurlSuccess: fmt.Sprintf("curl -fsS -m 10 --retry 3 -o /dev/null %s", base),
		CurlStart:   fmt.Sprintf("curl -fsS -m 10 --retry 3 -o /dev/null %s/start", base),
		CurlFail:    fmt.Sprintf("curl -fsS -m 10 --retry 3 -o /dev/null %s/fail", base),
		CronExample: fmt.Sprintf("your-job && curl -fsS -m 10 --retry 3 -o /dev/null %s", base),
		HowTo: "Run the success ping (curl_success) at the END of the monitored job or agent run. " +
			"If it fails, hit the fail URL instead (curl_fail). For long or possibly-hung work, send the start ping first (curl_start) — " +
			"LastPing then alerts if the run overruns its grace window or never finishes. " +
			"Optionally append ?rid=<unique-run-id> to pair a start with its success/fail and record the run duration.",
	}

	out, _ := json.MarshalIndent(pi, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}
