package mcptools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// callTool invokes a named MCP tool via HandleMessage and returns the CallToolResult.
// The provided *APIClient is seeded into the context via ContextWithClient.
func callTool(t *testing.T, s *server.MCPServer, c *mcptools.APIClient, name string, args map[string]interface{}) *mcp.CallToolResult {
	t.Helper()
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      name,
			"arguments": args,
		},
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	ctx := mcptools.ContextWithClient(context.Background(), c)
	resp := s.HandleMessage(ctx, raw)
	jr, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)
	result, ok := jr.Result.(*mcp.CallToolResult)
	require.True(t, ok, "expected *mcp.CallToolResult, got %T", jr.Result)
	return result
}

// newTestServer builds an MCPServer with all tools registered.
func newTestServer(t *testing.T, pingHost string) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.1", server.WithToolCapabilities(true))
	mcptools.Register(s, pingHost)
	return s
}

// --- create_monitor ---

func TestCreateMonitor_Simple(t *testing.T) {
	var capturedBody []byte
	var capturedMethod string
	var capturedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"My Job","slug":"my-job","status":"new","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_monitor", map[string]interface{}{
		"name":          "My Job",
		"slug":          "my-job",
		"schedule_kind": "simple",
		"period_s":      float64(3600),
		"grace_s":       float64(300),
	})

	assert.False(t, result.IsError, "expected success")
	assert.Equal(t, "POST", capturedMethod)
	assert.Equal(t, "/api/v1/checks", capturedPath)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "My Job", body["name"])
	assert.Equal(t, "my-job", body["slug"])
	assert.Equal(t, "simple", body["schedule_kind"])
	assert.Equal(t, float64(3600), body["period_s"])
}

func TestCreateMonitor_UpsertNoteInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // 200 = upsert (existing slug matched)
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"My Job","slug":"my-job","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_monitor", map[string]interface{}{
		"name": "My Job", "slug": "my-job", "schedule_kind": "simple", "period_s": float64(3600),
	})

	assert.False(t, result.IsError)
	// Result text must mention "updated" when HTTP 200 (upsert)
	text := extractText(result)
	assert.Contains(t, text, "updated")
}

func TestCreateMonitor_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"schedule_kind='cron' requires cron_expr"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_monitor", map[string]interface{}{
		"name": "My Job", "schedule_kind": "cron",
	})

	assert.True(t, result.IsError)
	text := extractText(result)
	assert.Contains(t, text, "cron_expr")
}

// --- list_monitors ---

func TestListMonitors(t *testing.T) {
	var capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"abc-123","name":"Job 1","slug":"job-1","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}]`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_monitors", map[string]interface{}{})
	assert.False(t, result.IsError)
	assert.Equal(t, "GET", capturedMethod)
	assert.Contains(t, extractText(result), "Job 1")
}

// --- get_monitor ---

func TestGetMonitor(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job 1","slug":"job-1","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_monitor", map[string]interface{}{"id": "abc-123"})
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/v1/checks/abc-123", capturedPath)
}

// --- update_monitor ---

func TestUpdateMonitor(t *testing.T) {
	var capturedBody []byte
	var capturedMethod string
	var capturedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Renamed","slug":"job-1","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":7200,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "update_monitor", map[string]interface{}{
		"id":            "abc-123",
		"name":          "Renamed",
		"schedule_kind": "simple",
		"period_s":      float64(7200),
		"grace_s":       float64(300),
	})

	assert.False(t, result.IsError, "expected success")
	assert.Equal(t, "PATCH", capturedMethod)
	assert.Equal(t, "/api/v1/checks/abc-123", capturedPath)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "Renamed", body["name"])
	assert.Equal(t, float64(7200), body["period_s"])
}

// --- delete_monitor ---

func TestDeleteMonitor(t *testing.T) {
	var capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "delete_monitor", map[string]interface{}{"id": "abc-123"})
	assert.False(t, result.IsError)
	assert.Equal(t, "DELETE", capturedMethod)
}

// --- pause_monitor ---

func TestPauseMonitor(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job 1","slug":"job-1","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":true,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "pause_monitor", map[string]interface{}{"id": "abc-123"})
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/v1/checks/abc-123/pause", capturedPath)
}

// --- resume_monitor ---

func TestResumeMonitor(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job 1","slug":"","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "resume_monitor", map[string]interface{}{"id": "abc-123"})
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/v1/checks/abc-123/resume", capturedPath)
}

// --- snooze_monitor ---

func TestSnoozeMonitor_Duration(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job 1","slug":"","status":"up","ping_url":"https://ping.lastping.dev/abc-123","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "snooze_monitor", map[string]interface{}{
		"id": "abc-123", "duration": "1h",
	})
	assert.False(t, result.IsError)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "1h", body["duration"])
}

// extractText returns the concatenated text from a CallToolResult.
func extractText(r *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// --- list_incidents ---

func TestListIncidents(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"opened_at":"2026-07-10T03:10:00Z","closed_at":null,"cause":"late","detail":""}]`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_incidents", map[string]interface{}{"id": "abc-123"})
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/v1/checks/abc-123/incidents", capturedPath)
	assert.Contains(t, extractText(result), "late")
}

func TestListIncidents_WithLimit(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	callTool(t, s, c, "list_incidents", map[string]interface{}{"id": "abc-123", "limit": float64(10)})
	assert.Contains(t, capturedQuery, "limit=10")
}

// --- list_destinations ---

func TestListDestinations(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"chan-1","kind":"email","name":"Ops email","target":"ops@example.com","verified":true,"disabled":false,"created_at":"2026-07-01T12:00:00Z"}]`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_destinations", map[string]interface{}{})
	assert.False(t, result.IsError)
	assert.Equal(t, "/api/v1/channels", capturedPath)
	assert.Contains(t, extractText(result), "Ops email")
}
