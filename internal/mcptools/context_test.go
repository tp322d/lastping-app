package mcptools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// TestContextWithClient_RoundTrip verifies that ContextWithClient produces a
// context that is distinct from the base context (i.e., the value is stored).
func TestContextWithClient_RoundTrip(t *testing.T) {
	c := mcptools.NewAPIClient("https://app.lastping.dev", "test-key")
	ctx := mcptools.ContextWithClient(context.Background(), c)

	require.NotNil(t, ctx)
	require.NotEqual(t, context.Background(), ctx, "context should be enriched with the client")

	// A second call with a different client should produce a distinct context value.
	c2 := mcptools.NewAPIClient("https://other.example.com", "other-key")
	ctx2 := mcptools.ContextWithClient(context.Background(), c2)
	assert.NotEqual(t, ctx, ctx2, "different clients should yield different contexts")
}

// TestContextWithClient_AbsentClientError verifies that a tool handler returns
// IsError=true when no client has been seeded into the context (rather than
// panicking or silently using a zero-value client).
func TestContextWithClient_AbsentClientError(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.1", server.WithToolCapabilities(true))
	mcptools.Register(s, "https://ping.lastping.dev")

	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "list_monitors",
			"arguments": map[string]interface{}{},
		},
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	// context.Background() has no client — the handler must return IsError=true.
	resp := s.HandleMessage(context.Background(), raw)

	jr, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)
	result, ok := jr.Result.(*mcp.CallToolResult)
	require.True(t, ok, "expected *mcp.CallToolResult, got %T", jr.Result)
	assert.True(t, result.IsError, "expected error when no client in context")
}
