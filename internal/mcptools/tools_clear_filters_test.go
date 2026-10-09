package mcptools_test

// update_monitor removes probe_expected_body, ci_workflow and ci_branch with
// the clear argument, which reaches the PATCH as JSON null (the API reads an
// empty string for these three as "unchanged"). The three keep a plain
// "string" schema: a union type ["string","null"] is refused by some clients'
// function-declaration formats, which fail the whole request. A client that
// sends the STRING "null" instead is refused for the two CI filters, where the
// API would otherwise store a filter literally named "null". These tests drive
// the real tools/list and tools/call JSON-RPC path and pin what reaches the API.

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

// ciFilters are the clearable filters for which a null-looking string is
// refused: a CI filter literally named "null" is never what the caller meant.
var ciFilters = []string{"ci_workflow", "ci_branch"}

// writeCapture is a fake API that records every PATCH or POST body it receives.
type writeCapture struct {
	srv    *httptest.Server
	writes atomic.Int32
	body   []byte
}

func newWriteCapture(t *testing.T) *writeCapture {
	t.Helper()
	wc := &writeCapture{}
	wc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/assertions") || strings.HasSuffix(r.URL.Path, "/guards") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.Method == http.MethodPatch || r.Method == http.MethodPost {
			wc.writes.Add(1)
			wc.body, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(`{"id":"abc-123","name":"Job","status":"up","created_at":"2026-07-17T00:00:00Z"}`))
	}))
	t.Cleanup(wc.srv.Close)
	return wc
}

func (wc *writeCapture) decoded(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(wc.body, &body))
	return body
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

// Every property of update_monitor served through tools/list has a plain
// string "type", never a union: a union type is refused by some clients'
// function-declaration formats, which then fail every tool, not just this one.
func TestUpdateMonitorSchemaKeepsPlainStringTypes(t *testing.T) {
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
		for _, key := range append([]string{"clear"}, clearableFilters...) {
			prop, ok := tool.InputSchema.Properties[key]
			require.True(t, ok, "update_monitor must declare %s", key)
			assert.Equal(t, "string", prop.Type, "update_monitor.%s must have the plain type \"string\"", key)
		}
		for key, prop := range tool.InputSchema.Properties {
			_, isString := prop.Type.(string)
			assert.True(t, isString, "update_monitor.%s has a non-scalar type %v", key, prop.Type)
		}
	}
	require.True(t, found, "update_monitor missing from tools/list")
}

func TestUpdateMonitorClearRemovesEachFilter(t *testing.T) {
	for _, key := range clearableFilters {
		t.Run(key, func(t *testing.T) {
			wc := newWriteCapture(t)
			c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

			result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","clear":"`+key+`"}`)
			require.False(t, result.IsError, extractText(result))
			require.EqualValues(t, 1, wc.writes.Load())

			body := wc.decoded(t)
			raw, present := body[key]
			require.True(t, present, "%s: clear must send JSON null so merge-patch removes the stored value", key)
			assert.Nil(t, raw, key)
			for _, other := range clearableFilters {
				if other != key {
					assert.NotContains(t, body, other, "clearing %s must leave %s unchanged", key, other)
				}
			}
		})
	}
}

// Several names, with stray spaces and capitals, all clear.
func TestUpdateMonitorClearAcceptsAList(t *testing.T) {
	wc := newWriteCapture(t)
	c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor",
		`{"id":"abc-123","name":"Job","clear":" ci_workflow , CI_BRANCH,probe_expected_body,"}`)
	require.False(t, result.IsError, extractText(result))

	body := wc.decoded(t)
	for _, key := range clearableFilters {
		raw, present := body[key]
		require.True(t, present, key)
		assert.Nil(t, raw, key)
	}
}

func TestUpdateMonitorClearRefusesUnknownName(t *testing.T) {
	wc := newWriteCapture(t)
	c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","clear":"ci_branch,tags"}`)
	require.True(t, result.IsError, "an unknown clear name must be refused, not ignored")
	text := extractText(result)
	assert.Contains(t, text, `"tags"`)
	for _, key := range clearableFilters {
		assert.Contains(t, text, key, "the error must list the accepted names")
	}
	assert.EqualValues(t, 0, wc.writes.Load(), "nothing may reach the API on a refused call")
}

func TestUpdateMonitorClearWithValueConflicts(t *testing.T) {
	for _, key := range clearableFilters {
		t.Run(key, func(t *testing.T) {
			wc := newWriteCapture(t)
			c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

			result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","clear":"`+key+`","`+key+`":"main"}`)
			require.True(t, result.IsError, "clear and a value for the same setting must be refused")
			assert.Contains(t, extractText(result), key)
			assert.EqualValues(t, 0, wc.writes.Load(), "nothing may reach the API on a refused call")
		})
	}
}

// A client that sends JSON null directly still clears.
func TestUpdateMonitorJSONNullStillClearsEachFilter(t *testing.T) {
	wc := newWriteCapture(t)
	c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor",
		`{"id":"abc-123","name":"Job","probe_expected_body":null,"ci_workflow":null,"ci_branch":null}`)
	require.False(t, result.IsError, extractText(result))
	require.EqualValues(t, 1, wc.writes.Load())

	body := wc.decoded(t)
	for _, key := range clearableFilters {
		raw, present := body[key]
		require.True(t, present, "%s: JSON null must be forwarded so merge-patch clears the stored value", key)
		assert.Nil(t, raw, key)
	}
}

var nullLikeValues = []string{"null", " NULL ", "None", "nil", "undefined", "Undefined"}

func TestNullLikeCIFilterRefusedOnBothTools(t *testing.T) {
	for _, tool := range []string{"update_monitor", "create_monitor"} {
		for _, key := range ciFilters {
			for _, v := range nullLikeValues {
				t.Run(tool+"/"+key+"/"+v, func(t *testing.T) {
					wc := newWriteCapture(t)
					c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

					args := `{"id":"abc-123","name":"Job","` + key + `":"` + v + `"}`
					if tool == "create_monitor" {
						args = `{"name":"Job","slug":"job","ci_provider":"github","` + key + `":"` + v + `"}`
					}
					result := callRaw(t, c, tool, args)
					require.True(t, result.IsError, "%q must be refused, not stored as a filter", v)
					text := extractText(result)
					assert.Contains(t, text, `clear: "`+key+`"`, "the error must say how to remove the filter")
					assert.EqualValues(t, 0, wc.writes.Load(), "nothing may reach the API on a refused call")
				})
			}
		}
	}
}

// Positive companion: a filter value that merely contains null-ish text is
// an ordinary name and reaches the API on both tools.
func TestCIFilterLookalikeValuesAreForwarded(t *testing.T) {
	for _, tool := range []string{"update_monitor", "create_monitor"} {
		t.Run(tool, func(t *testing.T) {
			wc := newWriteCapture(t)
			c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

			args := `{"id":"abc-123","name":"Job","ci_workflow":"nullable-check","ci_branch":"none-fix"}`
			if tool == "create_monitor" {
				args = `{"name":"Job","slug":"job","ci_provider":"github","ci_workflow":"nullable-check","ci_branch":"none-fix"}`
			}
			result := callRaw(t, c, tool, args)
			require.False(t, result.IsError, extractText(result))
			body := wc.decoded(t)
			assert.Equal(t, "nullable-check", body["ci_workflow"])
			assert.Equal(t, "none-fix", body["ci_branch"])
		})
	}
}

// probe_expected_body is exempt from the refusal: "the body contains null" is
// a legitimate check, so the string "null" is forwarded as a value, not as a
// clear (JSON null) and not refused.
func TestUpdateMonitorForwardsTheStringNullAsExpectedBody(t *testing.T) {
	wc := newWriteCapture(t)
	c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor", `{"id":"abc-123","name":"Job","probe_expected_body":"null"}`)
	require.False(t, result.IsError, extractText(result))
	require.EqualValues(t, 1, wc.writes.Load())
	assert.Equal(t, "null", wc.decoded(t)["probe_expected_body"],
		"the string \"null\" must reach the API as the string, not as JSON null")
}

// Positive companion to the refusals: an ordinary value still sets.
func TestUpdateMonitorStringValueSetsEachFilter(t *testing.T) {
	wc := newWriteCapture(t)
	c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

	result := callRaw(t, c, "update_monitor",
		`{"id":"abc-123","name":"Job","probe_expected_body":"\"status\":\"ok\"","ci_workflow":"deploy.yml","ci_branch":"main"}`)
	require.False(t, result.IsError, extractText(result))

	body := wc.decoded(t)
	assert.Equal(t, `"status":"ok"`, body["probe_expected_body"])
	assert.Equal(t, "deploy.yml", body["ci_workflow"])
	assert.Equal(t, "main", body["ci_branch"])
}

func TestUpdateMonitorOmittedFiltersStayUnchanged(t *testing.T) {
	for _, args := range []string{
		`{"id":"abc-123","name":"Renamed"}`,
		`{"id":"abc-123","name":"Renamed","clear":""}`,
	} {
		wc := newWriteCapture(t)
		c := mcptools.NewAPIClient(wc.srv.URL, "test-key")

		result := callRaw(t, c, "update_monitor", args)
		require.False(t, result.IsError, extractText(result))
		require.EqualValues(t, 1, wc.writes.Load())

		body := wc.decoded(t)
		assert.Equal(t, "Renamed", body["name"], "the PATCH must still carry the fields that were sent")
		assert.NotContains(t, body, "clear", "clear is an MCP argument, never an API field")
		for _, key := range clearableFilters {
			assert.NotContains(t, body, key, "%s omitted must not appear in the merge-patch body (%s)", key, args)
		}
	}
}
