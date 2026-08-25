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

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// get_ping_instructions is a pure proxy: the payload (run_wrapper, hook_install,
// how_to, reporting_options, the curl snippets) is assembled server-side by
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
  "discovery_how_to": "Scan the repo, propose what you found, then POST /api/v1/discovery/reconcile..."
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

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
	require.False(t, result.IsError, "expected success")

	var served mcptools.PingInstructions
	require.NoError(t, json.Unmarshal([]byte(pingInstructionsJSON), &served))

	var got mcptools.PingInstructions
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))

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
// drops whatever the struct does not name. discovery_how_to is the same shape
// of risk and is covered here from the day it was added.
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

	result := callTool(t, s, c, "get_ping_instructions", map[string]interface{}{"id": "abc-123"})
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
		"discovery_how_to",
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
// it learns that the payload carries expectations_how_to and discovery_how_to at
// all. Nothing else in either repository compares the two strings, so a
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

	// Named explicitly, because these three are the whole reason the pin
	// exists: each is a payload field an agent only discovers by reading the
	// description, and each was added to the payload long after the first
	// version of this description was written.
	for _, mention := range []string{"expectations_how_to", "discovery_how_to", "reporting_options"} {
		assert.Contains(t, got, mention, "the description must still name %s", mention)
	}
}

// wantGetPingInstructionsDesc is the hosted server's get_ping_instructions
// description, copied verbatim. See the test above.
const wantGetPingInstructionsDesc = "" +
	"Get everything needed to make a monitor actually report: the ping URL, copy-paste check-in snippets, and the three " +
	"MECHANISMS for reporting, returned together. Call this right after create_monitor. " +
	"CHOOSE BY WHAT THE MONITORED THING IS — read `reporting_options` first and pick by that, rather than defaulting to the raw curl list: " +
	"`how_to` — the manual protocol — is the UNIVERSAL path: it works in any agent, any language, any tool, with no prerequisite, so it is the " +
	"default choice for any agent this applies to. Pair it with expect_every_s (the silence floor, set via update_monitor) so an agent that " +
	"quietly stops reporting opens a detected incident instead of leaving its monitor reading healthy. " +
	"If you ARE Claude Code specifically, `hook_install` is available as an OPTIONAL SHORTCUT, not a better tier: a one-time install that binds " +
	"reporting to Claude Code's own hooks (UserPromptSubmit, Stop, StopFailure), automating how_to's exact same protocol so reporting becomes a " +
	"property of your event loop instead of something you must remember — and it is the only mechanism that can send every state this product " +
	"models, including blocked and note. hook_install is Claude Code specific: if you are a DIFFERENT AI agent — even one with its own hook or " +
	"event system, Cursor, Windsurf, Codex, a custom framework — do NOT translate its steps into your own hooks; the event semantics differ and a " +
	"translated install can pass its own verification while never reporting, so use `how_to` instead. " +
	"If what you are monitoring is launched as a command instead — a cron job, a CI step, a script, or an agent started from a shell — use " +
	"`run_wrapper`: wrap the command with `lastping run` and a separate process reports for you, so nothing has to be remembered; the tradeoff is " +
	"that it reports the process's own lifecycle (start, success, fail, cancel) and has no way to send blocked or note. " +
	"Whichever you choose, the underlying protocol is the same: the success ping at the END of the work, the fail URL if it failed, " +
	"the start ping first for long or possibly-hung runs (this enables overrun / never-finished detection), " +
	"and a step (curl_step) as each stage completes so a run that wedges mid-way is caught by name rather than only when its whole budget expires. " +
	"Also read `expectations_how_to`: before you start work, use declare_run_expectations to say how THIS run should be judged when it closes — " +
	"a one-time, unchangeable commitment that replaces the run grading itself. " +
	"And `discovery_how_to`, which is about the OTHER jobs on this host or in this repo: how to find the scheduled work nobody is watching yet " +
	"and propose it, rather than monitoring only the one thing you were asked about."

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
