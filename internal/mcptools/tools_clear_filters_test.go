package mcptools_test

// update_monitor clears probe_expected_body, ci_workflow and ci_branch with an
// explicit JSON null. The schema used to declare them plain "string", so a
// client that validates arguments could not send null, and one sent the STRING
// "null" instead, which the API stored as a literal branch filter. These tests
// drive the real tools/list and tools/call JSON-RPC path and pin what reaches
// the PATCH body.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

var clearableFilters = []string{"probe_expected_body", "ci_workflow", "ci_branch"}

// ciFilters are the clearable filters for which the string "null" is refused:
// a CI filter literally named "null" is never what the caller meant.
var ciFilters = []string{"ci_workflow", "ci_branch"}

// patchCapture is a fake API that records every PATCH body it receives.
type patchCapture struct {
	srv     *httptest.Server
	patches atomic.Int32
	body    []byte
}

func newPatchCapture(t *testing.T) *patchCapture {
	t.Helper()
	pc := &patchCapture{}
	pc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/assertions") || strings.HasSuffix(r.URL.Path, "/guards") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.Method == http.MethodPatch {
			pc.patches.Add(1)
			pc.body, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job","status":"up","created_at":"2026-07-17T00:00:00Z"}`))
	}))
	t.Cleanup(pc.srv.Close)
	return pc
}

// callRaw sends a tools/call whose arguments are given as raw JSON, so a JSON
// null is exactly what a client puts on the wire.
func callRaw(t *testing.T, c *mcptools.APIClient, name, argsJSON string) *mcp.CallToolResult {
	t.Helper()
	s := newTestServer(t, "https://ping.lastping.dev")
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + argsJSON + `}}`
	resp := s.HandleMessage(mcptools.ContextWithClient(context.Background(), c), []byte(msg))
	jr, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)
	result, ok := jr.Result.(*mcp.CallToolResult)
	require.True(t, ok, "expected *mcp.CallToolResult, got %T", jr.Result)
	return result
}

// The schema a client sees through tools/list must admit null for the three
// clearable filters, or a schema-validating client cannot send the clear.
func TestUpdateMonitorSchemaAdmitsNullForClearableFilters(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	resp := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	raw, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]struct {
						Type any `json:"type"`
					} `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))

	found := false
	for _, tool := range decoded.Result.Tools {
		if tool.Name != "update_monitor" {
			continue
		}
		found = true
		for _, key := range clearableFilters {
			prop, ok := tool.InputSchema.Properties[key]
			require.True(t, ok, "update_monitor must declare %s", key)
			assert.Equal(t, []any{"string", "null"}, prop.Type,
				"update_monitor.%s must accept JSON null on the wire", key)
		}
		// Positive companion: an ordinary string argument keeps its plain type,
		// so the assertion above is not satisfied by every property.
		assert.Equal(t, "string", tool.InputSchema.Properties["name"].Type)
	}
	require.True(t, found, "update_monitor missing from tools/list")
}

func TestUpdateMonitorJSONNullClearsEachFilter(t *testing.T) {
	pc := newPatchCapture(t)
	c := mcptools.NewAPIClient(pc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor",
		`{"id":"abc-123","name":"Job","probe_expected_body":null,"ci_workflow":null,"ci_branch":null}`)
	require.False(t, result.IsError, extractText(result))
	require.EqualValues(t, 1, pc.patches.Load())

	var body map[string]any
	require.NoError(t, json.Unmarshal(pc.body, &body))
	for _, key := range clearableFilters {
		raw, present := body[key]
		require.True(t, present, "%s: JSON null must be forwarded so merge-patch clears the stored value", key)
		assert.Nil(t, raw, key)
	}
}

func TestUpdateMonitorRefusesTheStringNull(t *testing.T) {
	for _, key := range ciFilters {
		t.Run(key, func(t *testing.T) {
			pc := newPatchCapture(t)
			c := mcptools.NewAPIClient(pc.srv.URL, "test-key")

			result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","`+key+`":"null"}`)
			require.True(t, result.IsError, "the string \"null\" must be refused, not stored as a filter")
			text := extractText(result)
			assert.Contains(t, text, key)
			assert.Contains(t, text, "JSON null", "the error must say how to clear")
			assert.EqualValues(t, 0, pc.patches.Load(), "nothing may reach the API on a refused call")
		})
	}
}

// probe_expected_body is exempt from the refusal: "the body contains null" is
// a legitimate check, so the string "null" is forwarded as a value, not as a
// clear (JSON null) and not refused.
func TestUpdateMonitorForwardsTheStringNullAsExpectedBody(t *testing.T) {
	pc := newPatchCapture(t)
	c := mcptools.NewAPIClient(pc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","probe_expected_body":"null"}`)
	require.False(t, result.IsError, extractText(result))
	require.EqualValues(t, 1, pc.patches.Load())

	var body map[string]any
	require.NoError(t, json.Unmarshal(pc.body, &body))
	assert.Equal(t, "null", body["probe_expected_body"],
		"the string \"null\" must reach the API as the string, not as JSON null")
}

// Positive companion to the refusal: an ordinary value still sets.
func TestUpdateMonitorStringValueSetsEachFilter(t *testing.T) {
	pc := newPatchCapture(t)
	c := mcptools.NewAPIClient(pc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor",
		`{"id":"abc-123","name":"Job","probe_expected_body":"\"status\":\"ok\"","ci_workflow":"deploy.yml","ci_branch":"main"}`)
	require.False(t, result.IsError, extractText(result))

	var body map[string]any
	require.NoError(t, json.Unmarshal(pc.body, &body))
	assert.Equal(t, `"status":"ok"`, body["probe_expected_body"])
	assert.Equal(t, "deploy.yml", body["ci_workflow"])
	assert.Equal(t, "main", body["ci_branch"])
}

func TestUpdateMonitorOmittedFiltersStayUnchanged(t *testing.T) {
	pc := newPatchCapture(t)
	c := mcptools.NewAPIClient(pc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Renamed"}`)
	require.False(t, result.IsError, extractText(result))
	require.EqualValues(t, 1, pc.patches.Load())

	var body map[string]any
	require.NoError(t, json.Unmarshal(pc.body, &body))
	assert.Equal(t, "Renamed", body["name"], "the PATCH must still carry the fields that were sent")
	for _, key := range clearableFilters {
		assert.NotContains(t, body, key, "%s omitted must not appear in the merge-patch body", key)
	}
}
