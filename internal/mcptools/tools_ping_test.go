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

// get_ping_instructions is a pure proxy to GET
// /api/v1/checks/{id}/ping-instructions: it must call that exact endpoint and
// re-render whatever JSON payload it returns, rather than building any of the
// snippets itself. These tests assert the proxying, not any locally-derived
// content, since the tool is deliberately not supposed to have any.
func TestGetPingInstructions_ProxiesToPingInstructionsEndpoint(t *testing.T) {
	var capturedPath, capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"monitor_id": "abc-123",
			"monitor_name": "Nightly backup",
			"ping_url": "https://ping.lastping.dev/abc-123",
			"success_url": "https://ping.lastping.dev/abc-123",
			"start_url": "https://ping.lastping.dev/abc-123/start",
			"fail_url": "https://ping.lastping.dev/abc-123/fail",
			"step_url": "https://ping.lastping.dev/abc-123/step",
			"curl_success": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123",
			"curl_start": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123/start",
			"curl_fail": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123/fail",
			"curl_step": "curl -fsS -m 10 --retry 3 -o /dev/null 'https://ping.lastping.dev/abc-123/step?rid=<run-id>&step=<name>'",
			"cron_example": "your-job && curl -fsS https://ping.lastping.dev/abc-123",
			"run_example": "start, steps, then success or fail, all sharing one rid",
			"step_timeout_s": null,
			"reporting_options": "pick how_to, run_wrapper or hook_install based on what is being monitored",
			"run_wrapper": "lastping run --monitor abc-123 -- your-command",
			"hook_install": "one-time Claude Code hook install text",
			"how_to": "the universal manual protocol",
			"how_to_steps": "1. start 2. steps 3. success or fail"
		}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError, "expected success")

	// The proxy hits the ping-instructions sub-resource specifically, not the
	// plain check endpoint — that is the whole point of the split: the
	// snippet-building logic lives server-side, this client only relays it.
	assert.Equal(t, "GET", capturedMethod)
	assert.Equal(t, "/api/v1/checks/abc-123/ping-instructions", capturedPath)

	// Every field the server sent must survive the round-trip verbatim,
	// including the fields the OLD locally-built payload never had
	// (run_wrapper, hook_install, step_url, run_example, reporting_options,
	// how_to_steps) — proof this tool assembles nothing itself.
	text := resultText(t, result)
	for _, want := range []string{
		"https://ping.lastping.dev/abc-123/start",
		"https://ping.lastping.dev/abc-123/fail",
		"https://ping.lastping.dev/abc-123/step",
		"curl -fsS",
		"Nightly backup",
		"run_wrapper",
		"lastping run --monitor abc-123",
		"hook_install",
		"one-time Claude Code hook install text",
		"reporting_options",
		"run_example",
		"how_to_steps",
	} {
		assert.Contains(t, text, want)
	}
}

func TestGetPingInstructions_FallbackConstructsURL(t *testing.T) {
	// The API returns no ping_url — the tool must construct it from the ping
	// host. Every other field is left at its zero value: rebuilding them here
	// would mean re-deriving the server's own assembly logic, which is exactly
	// the duplication this proxy exists to avoid.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"monitor_id":"xyz-9","monitor_name":"Job","step_timeout_s":null}`))
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
