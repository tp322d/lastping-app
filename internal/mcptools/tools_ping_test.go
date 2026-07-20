package mcptools_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// resultText concatenates the text content of a tool result.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, ct := range r.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestGetPingInstructions(t *testing.T) {
	var capturedPath, capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Nightly backup","slug":"nightly","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":86400,"grace_s":3600,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError, "expected success")
	assert.Equal(t, "GET", capturedMethod)
	assert.Equal(t, "/api/v1/checks/abc-123", capturedPath)

	text := resultText(t, result)
	assert.Contains(t, text, "https://ping.lastping.dev/abc-123")
	assert.Contains(t, text, "https://ping.lastping.dev/abc-123/start")
	assert.Contains(t, text, "https://ping.lastping.dev/abc-123/fail")
	assert.Contains(t, text, "curl -fsS")
	assert.Contains(t, text, "Nightly backup")
}

func TestGetPingInstructions_FallbackConstructsURL(t *testing.T) {
	// API returns no ping_url — the tool must construct it from the ping host.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"xyz-9","name":"Job","slug":"job","status":"new","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "xyz-9"})
	require.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "https://ping.lastping.dev/xyz-9")
}

func TestGetPingInstructions_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "missing"})
	assert.True(t, result.IsError, "expected error result for 404")
}
