package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Route mirrors the LastPing route resource: which destinations receive a
// monitor's alerts for a given event type.
type Route struct {
	EventType  string   `json:"event_type"`
	ChannelIDs []string `json:"channel_ids"`
}

func registerRouteTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("set_route",
			mcp.WithDescription("Route a monitor's alerts for one event type to a set of destinations (channels). "+
				"THIS REPLACES THE WHOLE SET for that event type — every destination you leave out stops receiving that event, including ones somebody else configured. "+
				"CALL get_monitor FIRST and read its `routes` field: that is the monitor's current routing, and adding a destination means passing the existing ids PLUS the new one. "+
				"Pass an empty channel_ids to remove all routing for the event. Destinations must be verified and "+
				"enabled (email destinations must be confirmed first). Use list_destinations for IDs."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor (check) UUID.")),
			mcp.WithString("event_type", mcp.Required(), mcp.Description("One of eight: down (alert opened), recovery (alert cleared), "+
				"fail (explicit failure ping), every-run (one notification per completed run, success or failure), "+
				"success (fires only when a run completes successfully), started (fires when a run begins), "+
				"blocked (an agent reported it is waiting on a human — fires immediately, the moment the ping arrives; "+
				"this is separate from the 'blocked' INCIDENT that opens later only if the wait outlives blocked_timeout_s, "+
				"see create_monitor/update_monitor), note (a free-form annotation ping — never itself opens or clears an incident). "+
				"Prefer down/recovery/fail: they fire only on a state change. every-run, success, started, and note are not "+
				"state changes and are bounded only by how often the monitor runs (or how often the agent chooses to send them), "+
				"so they can be very chatty, and none of them is flap-damped. started is the chattiest of the bunch for CI-fed "+
				"monitors: GitHub maps both the workflow_run 'requested' and 'in_progress' webhook events to a start signal, so a "+
				"single CI run can emit more than one started event — this was observed in production, where a real run logged "+
				"two starts seconds apart. every-run, success, started, and note share one separate per-channel rate cap "+
				"(60/hour by default), so together they can no longer use up the budget that down/fail/recovery/blocked "+
				"need — but a chatty route on any one of the four can silently suppress its own notifications, and "+
				"its sibling informational types' notifications, once it exceeds that shared cap. blocked is deliberately NOT "+
				"in that shared group even though it is agent-reported rather than system-derived: a blocked agent needs a human, "+
				"so it draws on the protected down/fail/recovery budget instead, precisely so it cannot be starved by chatty "+
				"every-run/success/started/note traffic. Route informational types to a low-stakes destination, not to the one "+
				"that pages someone.")),
			mcp.WithString("channel_ids", mcp.Description("Comma-separated destination (channel) UUIDs to notify. Empty string clears the route.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			monitorID, err := req.RequireString("monitor_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			eventType, err := req.RequireString("event_type")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			ids, _ := req.GetArguments()["channel_ids"].(string)
			return c.setRoute(ctx, monitorID, eventType, splitIDs(ids))
		},
	)
}

// getRoutes reads a monitor's whole routing table (GET
// /api/v1/checks/{id}/routes), one entry per event type that has any
// destinations. It exists so getMonitor can splice routing into the read path:
// set_route replaces the full channel set for the event type it names, and an
// agent with no way to see the current set can only ever guess at it.
//
// It returns an error rather than a tool result because its caller degrades
// gracefully — a monitor read must not be lost because a sub-resource call
// failed.
func (c *APIClient) getRoutes(ctx context.Context, id string) ([]Route, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/routes", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.problem(resp)
	}

	var routes []Route
	if dErr := json.NewDecoder(resp.Body).Decode(&routes); dErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", dErr)
	}
	return routes, nil
}

// splitIDs turns a comma-separated list into a trimmed, non-empty slice.
// A blank input yields an empty (non-nil) slice, which clears the route.
func splitIDs(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (c *APIClient) setRoute(ctx context.Context, monitorID, eventType string, channelIDs []string) (*mcp.CallToolResult, error) {
	body, _ := json.Marshal(map[string]interface{}{"channel_ids": channelIDs})

	url := fmt.Sprintf("%s/api/v1/checks/%s/routes/%s", c.BaseURL, monitorID, eventType)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", monitorID)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var route Route
	if err := json.NewDecoder(resp.Body).Decode(&route); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(route.ChannelIDs) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("Cleared all %q routing for monitor %s.", route.EventType, monitorID)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Routed %q alerts for monitor %s to %d destination(s): %s.",
		route.EventType, monitorID, len(route.ChannelIDs), strings.Join(route.ChannelIDs, ", "))), nil
}
