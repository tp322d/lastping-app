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
