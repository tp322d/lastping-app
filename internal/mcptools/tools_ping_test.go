package mcptools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lastping-dev/lastping-app/internal/mcptools"
)

// get_ping_instructions is a proxy: the payload (run_wrapper, how_to,
// reporting_options, the curl snippets, and hook_install when `tool` is set) is
// assembled server-side by
// GET /api/v1/checks/{id}/ping-instructions, never in this process. See ping.go's
// doc comment for why: the assembly reaches into a private prompt-building
// package this open-source binary must never carry. These tests therefore
// exercise the proxy's own behaviour — which endpoint it calls, that it decodes
// and re-renders the API's response faithfully with HTML escaping off, and how
// it handles 404 — not the content of the mechanisms themselves.

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

// pingInstructionsJSON is a representative GET .../ping-instructions response
// body, standing in for whatever the hosted API actually produces — this
// package cannot call that assembly itself without carrying the private
// prompt-building code the whole proxy split exists to keep out.
//
// It is the single fixture for every test in this file on purpose: two fixtures
// for one payload is how a field ends up present in the one a test reads and
// absent from the one the guard reads.
const pingInstructionsJSON = `{
  "monitor_id": "abc-123",
  "monitor_name": "Nightly backup",
  "ping_url": "https://ping.lastping.dev/abc-123",
  "success_url": "https://ping.lastping.dev/abc-123",
  "start_url": "https://ping.lastping.dev/abc-123/start",
  "fail_url": "https://ping.lastping.dev/abc-123/fail",
  "step_url": "https://ping.lastping.dev/abc-123/step?rid=<run-id>&step=<step-name>",
  "curl_success": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123",
  "curl_start": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123/start",
  "curl_fail": "curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123/fail",
  "curl_step": "curl -fsS -m 10 --retry 3 -o /dev/null \"https://ping.lastping.dev/abc-123/step?rid=$RID&step=db+migrate\"",
  "cron_example": "your-job && curl -fsS -m 10 --retry 3 -o /dev/null https://ping.lastping.dev/abc-123",
  "run_example": "RID=$(uuidgen)\ncurl .../start?rid=$RID\ncurl .../step?rid=$RID&step=dump\ncurl .../step?rid=$RID&step=upload\ncurl ...?rid=$RID",
  "step_timeout_s": 300,
  "reporting_options": "Three ways to report. how_to is the UNIVERSAL path... hook_install is an OPTIONAL SHORTCUT for Claude Code, run_wrapper fits a command.",
  "run_wrapper": "lastping run -- abc-123 your-command",
  "hook_install": "Merge this into ~/.claude/settings.json (MERGE, do not replace)",
  "how_to": "You are monitored by LastPing as the agent \"Nightly backup\"...",
  "how_to_steps": "Stall detection is ARMED on this monitor: step_timeout_s=300.",
  "expectations_how_to": "Call declare_run_expectations before you start work...",
  "failure_inbox_how_to": "Check GET /api/v1/agents/{id}/open-incidents before you start work...",
  "discovery_how_to": "Scan the repo, propose what you found, then POST /api/v1/discovery/reconcile...",
  "otel_traces_endpoint": "https://ping.lastping.dev/v1/traces",
  "otel_resource_attributes": "lastping.monitor_id=abc-123",
  "otel_headers_hint": "Authorization=Bearer <your tracing key>",
  "otel_env_lines": ["export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=\"https://ping.lastping.dev/v1/traces\""],
  "tracing_how_to": "TRACING: send OpenTelemetry traces to the LastPing monitor... fetch its set-up block with get_trace_setup"
}`

// TestGetPingInstructions_ProxiesToTheRightEndpoint verifies the tool calls
// GET /api/v1/checks/{id}/ping-instructions — not GET /api/v1/checks/{id},
// which is what it called before this tool became a proxy — and that the
// API's response reaches the caller.
func TestGetPingInstructions_ProxiesToTheRightEndpoint(t *testing.T) {
	var capturedPath, capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError, "expected success")
	assert.Equal(t, "GET", capturedMethod)
	assert.Equal(t, "/api/v1/checks/abc-123/ping-instructions", capturedPath)
}

// TestGetPingInstructions_ProxiesAPIResponseVerbatim decodes what the tool
// returned and asserts it equals what the fake API served, field for field —
// the proxy's whole job: it must not mangle, drop, or substitute anything on
// the way through.
func TestGetPingInstructions_ProxiesAPIResponseVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123", "tool": "claude-code"})
	require.False(t, result.IsError, "expected success")

	var served mcptools.PingInstructions
	require.NoError(t, json.Unmarshal([]byte(pingInstructionsJSON), &served))

	var got mcptools.PingInstructions
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))

	// docs_url is the one field this tool adds.
	served.DocsURL = "https://lastping.dev/mcp/"
	assert.Equal(t, served, got, "the tool must return exactly what the API served, not a rebuilt or partial copy")
}

// TestGetPingInstructions_IncludesEveryHowToField guards against
// PingInstructions (this repository's mirror of the API's ping-instructions
// struct) falling out of sync with it again by silently dropping a field.
//
// This has happened, and it is invisible when it does. failure_inbox_how_to was
// added to the API struct and not to this mirror, so the MCP server served a
// payload missing the one field that drives adoption of the whole failure loop
// -- no error, no warning, because a proxy that decodes into a struct simply
// drops whatever the struct does not name. The same risk applies to every other
// field the struct mirrors; discovery_how_to is no longer one of them (the tool
// drops it, see TestGetPingInstructions_OmitsDiscoveryHowTo).
//
// Unlike TestGetPingInstructions_ProxiesAPIResponseVerbatim, which decodes both
// the fixture and the tool's output through mcptools.PingInstructions and so
// would not notice a field missing from *both* sides equally, this test decodes
// the rendered text into a generic map: a field removed from the struct is
// decoded into nothing, re-encoded without it, and fails here on a missing key.
//
// The table is every instructional field on the payload, not only the two that
// have been forgotten before -- the next one to be dropped is by definition the
// one nobody thought to list.
func TestGetPingInstructions_IncludesEveryHowToField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123", "tool": "claude-code"})
	require.False(t, result.IsError, "expected success")

	var served map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(pingInstructionsJSON), &served))

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))

	for _, field := range []string{
		"reporting_options",
		"run_wrapper",
		"hook_install",
		"how_to",
		"how_to_steps",
		"expectations_how_to",
		"failure_inbox_how_to",
		"otel_traces_endpoint",
		"otel_resource_attributes",
		"otel_headers_hint",
		"otel_env_lines",
		"tracing_how_to",
	} {
		require.Contains(t, served, field, "fixture is missing %s; the assertion below would be vacuous", field)
		assert.Equal(t, served[field], got[field],
			"%s must round-trip through the proxy, not be dropped -- add it to mcptools.PingInstructions", field)
	}
}

// TestGetPingInstructions_DescriptionMatchesHostedServer pins the tool
// description byte-for-byte.
//
// The hosted MCP server and this binary are one product: an agent must get the
// same guidance whichever it connects to, and the description is the only place
// it learns that the payload carries expectations_how_to, hook_install_note
// and docs_url at all. Nothing else in either repository compares the two strings, so a
// paraphrase on one side is invisible until an agent behaves differently
// depending on which server it happened to reach. The literal below is a
// deliberate second copy of the string in ping.go: a test that referenced the
// same constant would assert nothing.
//
// If this fails, the description changed. Copy the hosted server's string here
// verbatim -- do not reword either side to make them meet in the middle.
func TestGetPingInstructions_DescriptionMatchesHostedServer(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")

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
	listed, ok := jr.Result.(mcp.ListToolsResult)
	require.True(t, ok, "expected ListToolsResult, got %T", jr.Result)

	var got string
	var found bool
	for _, tl := range listed.Tools {
		if tl.Name == "get_ping_instructions" {
			got, found = tl.Description, true
			break
		}
	}
	require.True(t, found, "get_ping_instructions is not registered")

	assert.Equal(t, wantGetPingInstructionsDesc, got,
		"get_ping_instructions' description has drifted from the hosted server's")

	// Named explicitly, because these are the whole reason the pin exists:
	// each is a payload field an agent only discovers by reading the
	// description, and each was added to the payload long after the first
	// version of this description was written.
	for _, mention := range []string{"expectations_how_to", "hook_install_note", "docs_url", "reporting_options"} {
		assert.Contains(t, got, mention, "the description must still name %s", mention)
	}
}

// wantGetPingInstructionsDesc is the hosted server's get_ping_instructions
// description, copied verbatim. See the test above.
const wantGetPingInstructionsDesc = "" +
	// The hosted server PREPENDS the required API key scope to every tool
	// description (scopes.go), so the pin starts with it too. Dropping it here
	// would make this test pass against a binary that stopped telling agents
	// which credential the tool needs.
	"Requires the read scope or higher. " +
	"Returns what a monitor needs in order to report: its ping URLs, copy-paste snippets (curl_success, curl_start, curl_fail, curl_step, run_example) " +
	"and three reporting mechanisms, for wiring up a new monitor. `reporting_options` holds the rule for choosing between them: " +
	"`how_to` is the manual protocol and works in any agent with no prerequisite (with expect_every_s, a lapse opens an incident); " +
	"`hook_install` is a one-time install that automates the same protocol through hooks and alone sends every state, blocked and note included; " +
	"it is returned only when `tool` is set (claude-code, codex or antigravity), otherwise `hook_install_note` says so; `run_wrapper` puts `lastping run` before a launched command (cron job, CI step, script) " +
	"and reports start, success, fail and cancel. Also returned: `failure_inbox_how_to`, `expectations_how_to` (declare_run_expectations), " +
	"`tracing_how_to` (OpenTelemetry, detailed by get_trace_setup), `otel_env_lines`, export lines whose key placeholder stands for the person's tracing key, and `docs_url`. " +
	"An exporter that cannot set headers can POST to `<ping_url>/v1/traces`, which needs no Authorization header."

// TestGetPingInstructions_PreservesLiteralAmpersandsAndPlaceholders guards the
// HTML-escaping-off requirement (marshalSnippets): every URL in this payload
// is a shell command meant to be copied verbatim, and encoding/json's default
// HTML escaping would turn `&` into `&` and `<`/`>` into `<`/`>`,
// making every step URL and placeholder unpasteable.
func TestGetPingInstructions_PreservesLiteralAmpersandsAndPlaceholders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "https://ping.lastping.dev/abc-123/step?rid=<run-id>&step=<step-name>")
	assert.Contains(t, text, "step?rid=$RID&step=db+migrate")
	assert.NotContains(t, text, "\\u0026", "escaped ampersand: the step URLs are not pasteable")
	assert.NotContains(t, text, "\\u003c", "escaped angle bracket: the placeholders are unreadable")
}

// TestGetPingInstructions_FallbackConstructsURL: on the off chance the API
// response carries no ping_url (it always does in production, but this proxy
// still has to degrade sanely rather than hand back an empty string), the tool
// backfills ping_url from pingHost.
func TestGetPingInstructions_FallbackConstructsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"monitor_id":"xyz-9","monitor_name":"Job","ping_url":"","step_timeout_s":null,"reporting_options":"","run_wrapper":"","hook_install":"","how_to":"","how_to_steps":""}`))
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

// TestGetPingInstructions_NeverRendersTraceSetupBlocks: the eight per-tool
// set-up blocks are get_trace_setup's, not this tool's, so every
// get_ping_instructions call stays small. Even a server that still serves
// trace_setup does not get them through: the mirror struct does not name the
// field. Positive companion: tracing_how_to, which points at
// get_trace_setup, still arrives.
func TestGetPingInstructions_NeverRendersTraceSetupBlocks(t *testing.T) {
	older := strings.TrimSuffix(strings.TrimSpace(pingInstructionsJSON), "}") +
		`, "trace_setup": [{"tool":"otel-sdk","title":"Any OpenTelemetry SDK","limits":"a block only get_trace_setup returns"}]}`
	var probe map[string]any
	require.NoError(t, json.Unmarshal([]byte(older), &probe), "the older-server fixture must be valid JSON")
	require.Contains(t, probe, "trace_setup")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(older))
	}))
	defer srv.Close()

	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"),
		"get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError)
	text := resultText(t, result)
	assert.NotContains(t, text, `"trace_setup":`)
	assert.NotContains(t, text, "a block only get_trace_setup returns")
	assert.Contains(t, text, `"tracing_how_to"`)
	assert.Contains(t, text, "get_trace_setup")
}

// TestGetPingInstructions_ToolIsSentAsHookTool: `tool` reaches the API as
// ?hook_tool=, and a call without it sends no query at all (the positive
// companion: the default install is the API's to choose).
func TestGetPingInstructions_ToolIsSentAsHookTool(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	require.False(t, callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123", "tool": "codex"}).IsError)
	require.False(t, callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"}).IsError)
	assert.Equal(t, []string{"hook_tool=codex", ""}, queries)
}

func TestAntigravityIsAnEnumValueOfPingAndTraceTools(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	for _, name := range []string{"get_ping_instructions", "get_trace_setup"} {
		st, ok := s.ListTools()[name]
		require.True(t, ok, "%s is not registered", name)
		schema, ok := st.Tool.InputSchema.Properties["tool"].(map[string]any)
		require.True(t, ok, "%s has no tool parameter", name)
		if name == "get_ping_instructions" {
			require.Equal(t, []string{"claude-code", "codex", "antigravity"}, schema["enum"])
			continue
		}
		require.Contains(t, schema["enum"], "antigravity")
		require.Contains(t, schema["description"], "gemini, antigravity, cursor")
	}
}

// pingInstructionsFor serves pingInstructionsJSON through the real tool handler
// and returns the decoded result plus the query the API received.
func pingInstructionsFor(t *testing.T, args map[string]interface{}) (map[string]interface{}, string) {
	t.Helper()
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pingInstructionsJSON))
	}))
	defer srv.Close()
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")
	result := callTool(t, s, c, "get_ping_instructions", args)
	require.False(t, result.IsError)
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))
	return got, query
}

func TestGetPingInstructions_NoToolOmitsHookInstallAndSaysWhy(t *testing.T) {
	got, _ := pingInstructionsFor(t, map[string]interface{}{"id": "abc-123"})
	assert.NotContains(t, got, "hook_install")
	assert.Equal(t, "Returned when the tool argument names claude-code, codex or antigravity.", got["hook_install_note"])
	// Positive companions: the rest of the payload is still there.
	assert.Contains(t, got, "failure_inbox_how_to")
	assert.Contains(t, got, "how_to")
}

func TestGetPingInstructions_ToolReturnsHookInstallWithoutNote(t *testing.T) {
	for _, tool := range []string{"claude-code", "codex", "antigravity"} {
		got, query := pingInstructionsFor(t, map[string]interface{}{"id": "abc-123", "tool": tool})
		assert.Equal(t, "hook_tool="+tool, query)
		assert.NotEmpty(t, got["hook_install"], tool)
		assert.NotContains(t, got, "hook_install_note", tool)
	}
}

func TestGetPingInstructions_OmitsDiscoveryHowTo(t *testing.T) {
	for _, args := range []map[string]interface{}{
		{"id": "abc-123"},
		{"id": "abc-123", "tool": "claude-code"},
	} {
		got, _ := pingInstructionsFor(t, args)
		assert.NotContains(t, got, "discovery_how_to")
		assert.Contains(t, got, "failure_inbox_how_to", "the installed hook's standing instruction points at this field")
	}
}

func TestGetPingInstructions_CarriesDocsURL(t *testing.T) {
	got, _ := pingInstructionsFor(t, map[string]interface{}{"id": "abc-123"})
	assert.Equal(t, "https://lastping.dev/mcp/", got["docs_url"])
}
