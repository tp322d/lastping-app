package mcptools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// --- get_run ---

// TestGetRun verifies get_run proxies to GET /api/v1/checks/{id}/runs/{rid}
// and wraps the run detail JSON in the untrusted-output envelope with the
// exact field set runs.go declares.
func TestGetRun(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"check_id": "abc-123", "check_name": "nightly ETL", "rid": "run-42",
			"title": "nightly ETL", "outcome": "failed",
			"started_at": "2026-07-20T09:58:00Z", "ended_at": "2026-07-20T10:00:00Z",
			"duration_ms": 120000, "step_count": 2, "exit_code": 1,
			"events": [
				{"kind": "start", "at": "2026-07-20T09:58:00Z", "label": "start"},
				{"kind": "fail", "at": "2026-07-20T10:00:00Z", "label": "fail", "body": "exit 1: boom"}
			],
			"steps": [
				{"seq": 1, "name": "extract", "at": "2026-07-20T09:58:30Z"}
			],
			"assertions": [
				{"seq": 1, "kind": "json", "path": "$.ok", "op": "eq", "value": "true", "verdict": "fail", "failure": "expected true, got false"}
			],
			"output_excerpt": "exit 1: boom",
			"ci": null,
			"events_truncated": false
		}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_run", map[string]interface{}{"id": "abc-123", "rid": "run-42"})
	assert.False(t, result.IsError, "expected success; got: %s", extractText(result))
	assert.Equal(t, "/api/v1/checks/abc-123/runs/run-42", capturedPath)

	var env struct {
		UntrustedFields []string        `json:"untrusted_fields"`
		Data            json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(result)), &env))
	assert.ElementsMatch(t, []string{"title", "output_excerpt", "rid",
		"events.body", "events.label", "steps.name", "assertions.failure",
		"spans.name", "spans.status_message", "spans.attributes",
		"spans.gen_ai.model", "spans.gen_ai.system",
		"source_name", "spans.source_name", "spans.peer_name", "agent_name"}, env.UntrustedFields)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(env.Data, &got))
	assert.Equal(t, "failed", got["outcome"])
	assert.Equal(t, "run-42", got["rid"])
}

// TestGetRun_NotFound verifies a 404 from the REST API becomes a tool error
// naming both the rid and the monitor id.
func TestGetRun_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"title":"Not Found","status":404,"detail":"run not found"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_run", map[string]interface{}{"id": "abc-123", "rid": "no-such-rid"})
	assert.True(t, result.IsError, "expected error result for 404")
	assert.Contains(t, extractText(result), "no-such-rid")
	assert.Contains(t, extractText(result), "abc-123")
}
