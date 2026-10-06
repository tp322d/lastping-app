package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	registerDeleteRouteTool(s)
	s.AddTool(
		newTool("set_route",
			mcp.WithDescription("Routes a monitor's alerts for one event type to a set of destinations (channels). "+
				"Replaces the whole destination set for that event type: destinations not listed stop receiving it, including ones someone else configured. "+
				"get_monitor's `routes` field holds the current set, so adding a destination means sending the existing ids plus the new one. "+
				"An empty channel_ids removes all routing for the event. Destinations have to be verified and enabled (an email one confirmed). "+
				"list_destinations returns the ids."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor (check) UUID.")),
			mcp.WithString("event_type", mcp.Required(), mcp.Description("down (incident opened), recovery (incident cleared), "+
				"fail (failure ping), every-run (each completed run), success, started (GitHub's requested and in_progress each send one, so one CI run can notify twice), "+
				"blocked (an agent awaits a human: held 10 minutes, cancelled by a ping or step of the same run in that time; sent once per blocked stretch; "+
				"not the 'blocked' incident of blocked_timeout_s), note (an annotation). "+
				"down, recovery and fail fire only on a state change. every-run, success, started and note share a separate per-channel rate cap "+
				"(60/hour default) and are not flap-damped, so one chatty route can suppress the group; "+
				"blocked draws on the protected down/fail/recovery budget.")),
			mcp.WithString("channel_ids", mcp.Description("Comma-separated destination (channel) UUIDs to notify. An empty string clears the route.")),
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

// routeEventTypes is every event type a route can carry, as the API
// validates it.
var routeEventTypes = []string{"down", "recovery", "fail", "every-run", "success", "started", "blocked", "note"}

// registerDeleteRouteTool registers delete_route, the proxy for DELETE
// /api/v1/checks/{id}/routes/{event_type}. set_route REPLACES the whole
// destination set of an event type, so before this tool unrouting one event
// meant calling set_route with an empty set and trusting nothing else was
// touched; delete_route names the one event type and cannot reach another.
func registerDeleteRouteTool(s *server.MCPServer) {
	s.AddTool(
		newTool("delete_route",
			mcp.WithDescription("Stops routing one event type of a monitor to any destination, so its alerts for that event go nowhere; "+
				"every other event type's routing is unchanged. set_route with the remaining ids drops a single destination instead. "+
				"An event type with no routing answers \"route not found\"."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor (check) UUID.")),
			mcp.WithString("event_type", mcp.Required(), mcp.Enum(routeEventTypes...),
				mcp.Description("The event type to unroute: down, recovery, fail, every-run, success, started, blocked or note.")),
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
			return c.deleteRoute(ctx, monitorID, eventType)
		},
	)
}

// deleteRoute issues DELETE /api/v1/checks/{id}/routes/{event_type}.
func (c *APIClient) deleteRoute(ctx context.Context, monitorID, eventType string) (*mcp.CallToolResult, error) {
	target := fmt.Sprintf("%s/api/v1/checks/%s/routes/%s", c.BaseURL, url.PathEscape(monitorID), url.PathEscape(eventType))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Removed the %q routing from monitor %s. Its other event types are routed as before.", eventType, monitorID)), nil
	}
	info, perr := c.problemDetail(resp)
	if resp.StatusCode == http.StatusNotFound {
		// Two different 404s: the monitor is not in this project, or it is
		// and has no routing for this event type. The detail tells them
		// apart, and the second is not something to retry.
		if info.Detail == "route not found" {
			return mcp.NewToolResultError(fmt.Sprintf(
				"Monitor %s has no %q routing, so there was nothing to remove. get_monitor shows its routes.", monitorID, eventType)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", monitorID)), nil
	}
	return mcp.NewToolResultError(perr.Error()), nil
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
