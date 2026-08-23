package mcptools_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// wantTools is the parity contract with the hosted MCP server: every tool the
// hosted server exposes must have a client here, and this list is the thing
// that must be updated (deliberately, by a human reading the diff) whenever a
// tool is added or removed. If this test fails, either a tool was dropped by
// accident or a new one was added without updating this list — both are bugs
// this test exists to catch.
var wantTools = []string{
	// agents.go (5)
	"register_agent",
	"list_agents",
	"get_agent",
	"update_agent",
	"delete_agent",
	// apikeys.go (3)
	"create_api_key",
	"list_api_keys",
	"revoke_api_key",
	// channels.go (5)
	"list_destinations",
	"create_destination",
	"update_destination",
	"test_destination",
	"delete_destination",
	// checks.go (8)
	"create_monitor",
	"list_monitors",
	"get_monitor",
	"update_monitor",
	"delete_monitor",
	"pause_monitor",
	"resume_monitor",
	"snooze_monitor",
	// export.go (1)
	"export_terraform",
	// incidents.go (2)
	"list_incidents",
	"get_run_history",
	// ping.go (1)
	"get_ping_instructions",
	// routes.go (1)
	"set_route",
	// run_expectations.go (1)
	"declare_run_expectations",
	// statuspages.go (4)
	"list_status_pages",
	"create_status_page",
	"update_status_page",
	"delete_status_page",
	// templates.go (2)
	"get_alert_templates",
	"set_alert_template",
}

// TestGoldenToolCount_Is33 is the regression guard for tool parity with the
// hosted MCP server. It fails if a tool is dropped or added without updating
// wantTools above.
func TestGoldenToolCount_Is33(t *testing.T) {
	require.Len(t, wantTools, 33, "wantTools itself must list exactly 33 tools — update it deliberately, alongside internal/mcptools, when parity changes")

	got := registeredToolNames(t)
	require.Len(t, got, 33, "the server must register exactly 33 tools")

	want := append([]string(nil), wantTools...)
	sort.Strings(want)
	sort.Strings(got)
	require.Equal(t, want, got, "registered tool set must exactly match wantTools")
}

// registeredToolNames drives the server's tools/list JSON-RPC method and
// returns every tool name it advertises — the same set an MCP client sees.
func registeredToolNames(t *testing.T) []string {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.1", server.WithToolCapabilities(true))
	mcptools.Register(s, "https://ping.lastping.dev")

	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]interface{}{},
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	resp := s.HandleMessage(context.Background(), raw)
	jr, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)
	result, ok := jr.Result.(mcp.ListToolsResult)
	require.True(t, ok, "expected ListToolsResult, got %T", jr.Result)

	names := make([]string, 0, len(result.Tools))
	for _, tl := range result.Tools {
		names = append(names, tl.Name)
	}
	return names
}
