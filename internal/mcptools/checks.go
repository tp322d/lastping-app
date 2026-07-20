package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Check mirrors the relevant fields of the LastPing Check resource.
type Check struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	Status       string  `json:"status"`
	ScheduleKind string  `json:"schedule_kind"`
	PeriodS      int64   `json:"period_s"`
	CronExpr     string  `json:"cron_expr"`
	TZ           string  `json:"tz"`
	GraceS       int64   `json:"grace_s"`
	Paused       bool    `json:"paused"`
	PingURL      string  `json:"ping_url"`
	MonitorType  string  `json:"monitor_type"`
	CreatedAt    string  `json:"created_at"`
	LastPingAt   *string `json:"last_ping_at"`
	DueAt        *string `json:"due_at"`
}

func registerCheckTools(s *server.MCPServer) {
	// create_monitor
	s.AddTool(
		mcp.NewTool("create_monitor",
			mcp.WithDescription("Create a new LastPing monitor (or update an existing one if slug matches — returns 'updated' note on upsert). "+
				"For heartbeat/ci monitors supply schedule_kind ('simple' requires period_s, 'cron' requires cron_expr). "+
				"For http monitors supply probe_url and probe_interval_s instead."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name, e.g. 'Daily backup job'.")),
			mcp.WithString("slug", mcp.Description("Optional stable ID. If a monitor with this slug exists, it will be updated (upsert).")),
			mcp.WithString("monitor_type", mcp.Description("'heartbeat' (default), 'ci', or 'http'.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple' (requires period_s) or 'cron' (requires cron_expr). Required for heartbeat/ci monitors.")),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds. Required when schedule_kind='simple'.")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression, e.g. '0 3 * * *'. Required when schedule_kind='cron'.")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation. Defaults to UTC.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds after a ping is due before alerting.")),
			mcp.WithString("probe_url", mcp.Description("URL to probe. Required when monitor_type='http'.")),
			mcp.WithNumber("probe_interval_s", mcp.Description("Probe interval in seconds (30–86400). Required when monitor_type='http'.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createMonitor(ctx, req)
		},
	)

	// list_monitors
	s.AddTool(
		mcp.NewTool("list_monitors",
			mcp.WithDescription("List all monitors in the authenticated LastPing project. Returns id, name, slug, status, ping_url for each.")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listMonitors(ctx)
		},
	)

	// get_monitor
	s.AddTool(
		mcp.NewTool("get_monitor",
			mcp.WithDescription("Get a single LastPing monitor by UUID."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getMonitor(ctx, id)
		},
	)

	// update_monitor
	s.AddTool(
		mcp.NewTool("update_monitor",
			mcp.WithDescription("Update an existing LastPing monitor's schedule/config by UUID (full replace of schedule fields; slug is preserved)."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple' or 'cron'.")),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds (for schedule_kind='simple').")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression (for schedule_kind='cron').")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds.")),
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
			return c.updateMonitor(ctx, id, req)
		},
	)

	// delete_monitor
	s.AddTool(
		mcp.NewTool("delete_monitor",
			mcp.WithDescription("Permanently delete a LastPing monitor by UUID. This cannot be undone."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.deleteMonitor(ctx, id)
		},
	)

	// pause_monitor
	s.AddTool(
		mcp.NewTool("pause_monitor",
			mcp.WithDescription("Pause a LastPing monitor so it stops alerting (paused=true). The monitor still receives pings but does not alert."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.simpleCheckPost(ctx, id, "pause")
		},
	)

	// resume_monitor
	s.AddTool(
		mcp.NewTool("resume_monitor",
			mcp.WithDescription("Resume a paused LastPing monitor (paused=false). Alerting resumes on the next missed ping."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.simpleCheckPost(ctx, id, "resume")
		},
	)

	// snooze_monitor
	s.AddTool(
		mcp.NewTool("snooze_monitor",
			mcp.WithDescription("Set or clear a maintenance window on a monitor. During the window the monitor will not alert. "+
				"Provide exactly one of: duration (e.g. '1h', '24h'), until (RFC 3339 timestamp), or clear=true to remove the window."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("duration", mcp.Description("Go duration string, e.g. '1h' or '24h'. Use this OR until OR clear.")),
			mcp.WithString("until", mcp.Description("RFC 3339 end timestamp. Use this OR duration OR clear.")),
			mcp.WithBoolean("clear", mcp.Description("Set true to remove the active maintenance window.")),
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
			return c.snoozeMonitor(ctx, id, req)
		},
	)
}

// --- APIClient methods for checks ---

func (c *APIClient) createMonitor(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	if v, ok := args["slug"].(string); ok && v != "" {
		body["slug"] = v
	}
	if v, ok := args["monitor_type"].(string); ok && v != "" {
		body["monitor_type"] = v
	}
	if v, ok := args["schedule_kind"].(string); ok && v != "" {
		body["schedule_kind"] = v
	}
	if v, ok := args["period_s"].(float64); ok && v > 0 {
		body["period_s"] = v
	}
	if v, ok := args["cron_expr"].(string); ok && v != "" {
		body["cron_expr"] = v
	}
	if v, ok := args["tz"].(string); ok && v != "" {
		body["tz"] = v
	}
	if v, ok := args["grace_s"].(float64); ok && v > 0 {
		body["grace_s"] = v
	}
	if v, ok := args["probe_url"].(string); ok && v != "" {
		body["probe_url"] = v
	}
	if v, ok := args["probe_interval_s"].(float64); ok && v > 0 {
		body["probe_interval_s"] = v
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	note := "created"
	if resp.StatusCode == http.StatusOK {
		note = "updated (upsert: existing monitor with this slug was updated)"
	}
	return mcp.NewToolResultText(fmt.Sprintf("Monitor %s (id=%s, status=%s, ping_url=%s)", note, ch.ID, ch.Status, ch.PingURL)), nil
}

func (c *APIClient) listMonitors(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks", nil)
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

	var checks []Check
	if err := json.NewDecoder(resp.Body).Decode(&checks); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(checks) == 0 {
		return mcp.NewToolResultText("No monitors found. Create one with create_monitor."), nil
	}

	out, _ := json.MarshalIndent(checks, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) getMonitor(ctx context.Context, id string) (*mcp.CallToolResult, error) {
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	out, _ := json.MarshalIndent(ch, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) updateMonitor(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	if v, ok := args["schedule_kind"].(string); ok && v != "" {
		body["schedule_kind"] = v
	}
	if v, ok := args["period_s"].(float64); ok && v > 0 {
		body["period_s"] = v
	}
	if v, ok := args["cron_expr"].(string); ok && v != "" {
		body["cron_expr"] = v
	}
	if v, ok := args["tz"].(string); ok && v != "" {
		body["tz"] = v
	}
	if v, ok := args["grace_s"].(float64); ok && v > 0 {
		body["grace_s"] = v
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/checks/"+id, bytes.NewReader(data))
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

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	out, _ := json.MarshalIndent(ch, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("Monitor updated:\n%s", out)), nil
}

func (c *APIClient) deleteMonitor(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/checks/"+id, nil)
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
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Monitor %s deleted successfully.", id)), nil
}

// simpleCheckPost is used for pause and resume (no request body, returns a Check).
func (c *APIClient) simpleCheckPost(ctx context.Context, id, action string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks/"+id+"/"+action, nil)
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

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Monitor %sd. paused=%v, status=%s", action, ch.Paused, ch.Status)), nil
}

func (c *APIClient) snoozeMonitor(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["duration"].(string); ok && v != "" {
		body["duration"] = v
	}
	if v, ok := args["until"].(string); ok && v != "" {
		body["until"] = v
	}
	if v, ok := args["clear"].(bool); ok && v {
		body["clear"] = true
	}

	if len(body) == 0 {
		return mcp.NewToolResultError("Provide exactly one of: duration (e.g. '1h'), until (RFC 3339 timestamp), or clear=true."), nil
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks/"+id+"/snooze", bytes.NewReader(data))
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

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Maintenance window set on monitor %s (status=%s).", id, ch.Status)), nil
}
