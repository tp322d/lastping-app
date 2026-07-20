package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Channel mirrors the LastPing Channel resource (no secrets).
type Channel struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Target        string `json:"target"`
	Verified      bool   `json:"verified"`
	Disabled      bool   `json:"disabled"`
	DisableReason string `json:"disable_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
}

func registerChannelTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("list_destinations",
			mcp.WithDescription("List all notification destinations (channels) in the project: email, webhook, Slack, Discord, Telegram. "+
				"Use channel IDs to configure routing rules for monitors.")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listChannels(ctx)
		},
	)
}

func (c *APIClient) listChannels(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/channels", nil)
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

	var channels []Channel
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(channels) == 0 {
		return mcp.NewToolResultText("No destinations found. Add one at https://app.lastping.dev under Project → Destinations."), nil
	}

	out, _ := json.MarshalIndent(channels, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}
