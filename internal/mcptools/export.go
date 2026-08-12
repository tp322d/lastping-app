package mcptools

// export.go — MCP tool wrapping GET /api/v1/export/terraform.
//
// Monitors, destinations, routes, etc. created imperatively via the other MCP
// tools are invisible to Terraform until they are exported and adopted with
// `terraform import`. This tool returns ready-to-commit HCL (with import
// blocks) so an agent can hand its work back to a Terraform-managed repo.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerExportTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("export_terraform",
			mcp.WithDescription("Export existing LastPing monitors, destinations, routes, "+
				"alert templates and status pages as Terraform HCL, including import blocks "+
				"so they are adopted rather than recreated. Secrets are NOT exported — the "+
				"output references Terraform variables you must fill in."),
			mcp.WithString("tag", mcp.Description("Optional tag to filter monitors by, e.g. 'agent:claude'. "+
				"Only monitors carrying this tag (and their routes/templates) are exported.")),
			mcp.WithString("monitor_slug", mcp.Description("Optional slug to export a single monitor by. "+
				"Combines with tag if both are given.")),
			mcp.WithString("include", mcp.Description("Optional comma-separated subset of "+
				"monitors,destinations,routes,templates,status_pages. Omit to export everything.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.exportTerraform(ctx,
				req.GetString("tag", ""),
				req.GetString("monitor_slug", ""),
				req.GetString("include", ""))
		},
	)
}

// exportTerraform calls GET /api/v1/export/terraform and returns the HCL body
// as tool text output.
func (c *APIClient) exportTerraform(ctx context.Context, tag, monitorSlug, include string) (*mcp.CallToolResult, error) {
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if monitorSlug != "" {
		q.Set("monitor_slug", monitorSlug)
	}
	if include != "" {
		q.Set("include", include)
	}

	reqURL := c.BaseURL + "/api/v1/export/terraform"
	if enc := q.Encode(); enc != "" {
		reqURL += "?" + enc
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	return mcp.NewToolResultText(string(body)), nil
}
