package mcptools

import "github.com/mark3labs/mcp-go/server"

// Register adds all LastPing MCP tools to s.
// pingHost is the base URL for ping endpoints (e.g. "https://ping.lastping.dev").
// Each tool handler resolves its *APIClient from the request context; callers must
// seed it via ContextWithClient before the server starts handling requests.
func Register(s *server.MCPServer, pingHost string) {
	registerCheckTools(s)
	registerIncidentTools(s)
	registerChannelTools(s)
	registerRouteTools(s)
	registerStatusPageTools(s)
	registerPingTools(s, pingHost)
	registerTemplateTools(s)
	registerAPIKeyTools(s)
	registerExportTools(s)
	registerAgentTools(s)
	registerRunExpectationTools(s)
}
