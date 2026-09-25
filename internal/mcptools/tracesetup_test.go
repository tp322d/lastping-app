package mcptools_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// traceSetupJSON is one block of GET /api/v1/checks/{id}/trace-setup, every
// field populated, so a field dropped from the mirror struct fails below.
const traceSetupJSON = `{"tool":"claude-code","prompt":"TRACING: send Claude Code's OpenTelemetry to the LastPing monitor \"x\".","blocks":[` +
	`{"tool":"claude-code","title":"Claude Code","writes":"The env block","command":"","steps":["a && b"],` +
	`"files":[{"path":"~/.claude/lastping-tracing-key","language":"text","content":"<your tracing key>\n","mode":"600","merge":false},` +
	`{"path":"~/.claude/settings.json","language":"json","content":"{}\n","mode":"","merge":true}],` +
	`"env_lines":["export OTEL_EXPORTER_OTLP_PROTOCOL=\"http/protobuf\""],"verify":"Send one test span. It must print 202:",` +
	`"console_link":"https://app.lastping.dev/app/runs?monitor=abc-123","limits":"Claude Code reads telemetry settings only when a session starts.",` +
	`"secret_in_url":false}]}`

// TestGetTraceSetup_ProxiesTheRouteAndDropsNothing. The tool calls exactly
// GET /api/v1/checks/{id}/trace-setup?tool=, and every field of the response
// survives the round trip (decoded into a generic map, so a field missing
// from BOTH the fixture and the mirror struct cannot pass), with the
// placeholder and shell `&&` left paste-ready.
func TestGetTraceSetup_ProxiesTheRouteAndDropsNothing(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(traceSetupJSON))
	}))
	defer srv.Close()

	s := newTestServer(t, "https://ping.lastping.dev")
	result := callTool(t, s, mcptools.NewAPIClient(srv.URL, "k"), "get_trace_setup",
		map[string]interface{}{"monitor_id": "abc-123", "tool": "claude-code"})
	require.False(t, result.IsError, resultText(t, result))
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/api/v1/checks/abc-123/trace-setup", gotPath)
	require.Equal(t, "tool=claude-code", gotQuery)

	var served, got map[string]any
	require.NoError(t, json.Unmarshal([]byte(traceSetupJSON), &served))
	text := resultText(t, result)
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	assert.Equal(t, served, got, "every field must round-trip through the proxy")
	assert.Contains(t, text, "<your tracing key>")
	assert.Contains(t, text, "a && b")

	// Omitting tool asks for every block: no query at all.
	callTool(t, s, mcptools.NewAPIClient(srv.URL, "k"), "get_trace_setup", map[string]interface{}{"monitor_id": "abc-123"})
	require.Equal(t, "", gotQuery)
}

// TestGetTraceSetup_NotFoundNamesTheFix.
func TestGetTraceSetup_NotFoundNamesTheFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"), "get_trace_setup",
		map[string]interface{}{"monitor_id": "nope"})
	require.True(t, result.IsError)
	require.Contains(t, resultText(t, result), "list_monitors")
}

// keyRecorder is a minting endpoint that records every request.
type keyRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	answer func(n int) (int, string)
}

func (k *keyRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		k.mu.Lock()
		k.paths = append(k.paths, r.Method+" "+r.URL.Path)
		k.bodies = append(k.bodies, body)
		n := len(k.paths)
		k.mu.Unlock()
		status, out := k.answer(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}
}

const mintedIngestJSON = `{"id":"key-9","name":"Tracing: x","prefix":"lp_tr4c3","created_at":"2026-09-25T00:00:00Z","scope":"ingest",` +
	`"expires_at":"2026-12-24T00:00:00Z","check_id":"abc-123","key":"lp_tr4c3_secret_value"}`

// TestCreateIngestKey_PostsOnlyNameAndExpiryToTheMonitorsRoute. The tool
// calls POST /api/v1/checks/{id}/ingest-keys, never /api/v1/api-keys, and
// its body carries name and expires_at and nothing else: no scope and no
// check_id, which the route fixes. An omitted expiry is sent as about 90
// days, as create_api_key does. The key is returned once, with the
// instruction not to repeat it.
func TestCreateIngestKey_PostsOnlyNameAndExpiryToTheMonitorsRoute(t *testing.T) {
	rec := &keyRecorder{answer: func(int) (int, string) { return http.StatusCreated, mintedIngestJSON }}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"), "create_ingest_key",
		map[string]interface{}{"monitor_id": "abc-123", "name": "laptop"})
	require.False(t, result.IsError, resultText(t, result))
	require.Equal(t, []string{"POST /api/v1/checks/abc-123/ingest-keys"}, rec.paths)
	body := rec.bodies[0]
	require.Equal(t, "laptop", body["name"])
	require.NotContains(t, body, "scope")
	require.NotContains(t, body, "check_id")
	exp, err := time.Parse(time.RFC3339, body["expires_at"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(90*24*time.Hour), exp, time.Hour)

	text := resultText(t, result)
	require.Contains(t, text, "lp_tr4c3_secret_value")
	require.Contains(t, text, "scope=ingest")
	require.Contains(t, text, "monitor=abc-123")
	require.Contains(t, text, "do not repeat it to the person")
	require.NotContains(t, text, "—")
	// When tracing stops is the person's to know: the expiry is in the
	// output with the instruction to say so.
	require.Contains(t, text, "expires_at=2026-12-24T00:00:00Z")
	require.Contains(t, text, "It expires at 2026-12-24T00:00:00Z. Tell the person that tracing will stop then")
}

// TestCreateIngestKey_ANeverExpiringKeyIsSaidToBeOne. A key with no expiry
// (a server with inheritance off and no expires_at) is reported as never
// expiring, not as an empty date.
func TestCreateIngestKey_ANeverExpiringKeyIsSaidToBeOne(t *testing.T) {
	rec := &keyRecorder{answer: func(int) (int, string) {
		return http.StatusCreated, strings.Replace(mintedIngestJSON, `"expires_at":"2026-12-24T00:00:00Z",`, "", 1)
	}}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"), "create_ingest_key",
		map[string]interface{}{"monitor_id": "abc-123"})
	require.False(t, result.IsError, resultText(t, result))
	text := resultText(t, result)
	require.Contains(t, text, "expires_at=never")
	require.Contains(t, text, "It does not expire")
	require.NotContains(t, text, "It expires at")
}

// TestCreateIngestKey_RetriesAtTheParentCeiling. Under a calling key that
// expires sooner than 90 days, the server refuses the default with
// max_expires_at; the tool retries once at that ceiling, as create_api_key
// does, and never lengthens the key's life.
func TestCreateIngestKey_RetriesAtTheParentCeiling(t *testing.T) {
	const ceiling = "2026-10-01T00:00:00Z"
	rec := &keyRecorder{answer: func(n int) (int, string) {
		if n == 1 {
			return http.StatusBadRequest, `{"detail":"expires_at may not exceed the creating key's own expiry","max_expires_at":"` + ceiling + `"}`
		}
		return http.StatusCreated, mintedIngestJSON
	}}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"), "create_ingest_key",
		map[string]interface{}{"monitor_id": "abc-123"})
	require.False(t, result.IsError, resultText(t, result))
	require.Len(t, rec.bodies, 2)
	require.Equal(t, ceiling, rec.bodies[1]["expires_at"])
	require.NotContains(t, rec.bodies[0], "name", "an omitted name is left to the server's default")
}

// TestCreateIngestKey_ARefusalIsSurfaced: a 403 (a read key) is an error the
// agent can read, never a retry at another route.
func TestCreateIngestKey_ARefusalIsSurfaced(t *testing.T) {
	rec := &keyRecorder{answer: func(int) (int, string) {
		return http.StatusForbidden, `{"detail":"this API key's scope does not allow this request","required_scope":"write"}`
	}}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	result := callTool(t, newTestServer(t, "https://ping.lastping.dev"), mcptools.NewAPIClient(srv.URL, "k"), "create_ingest_key",
		map[string]interface{}{"monitor_id": "abc-123"})
	require.True(t, result.IsError)
	require.Len(t, rec.paths, 1)
	require.True(t, strings.HasSuffix(rec.paths[0], "/ingest-keys"))
}

// TestCreateAPIKey_ForwardsCheckID. create_api_key gains an optional
// check_id, sent only when given.
func TestCreateAPIKey_ForwardsCheckID(t *testing.T) {
	rec := &keyRecorder{answer: func(int) (int, string) { return http.StatusCreated, mintedIngestJSON }}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	s := newTestServer(t, "https://ping.lastping.dev")
	callTool(t, s, mcptools.NewAPIClient(srv.URL, "k"), "create_api_key",
		map[string]interface{}{"name": "codex", "scope": "ingest", "check_id": "abc-123"})
	callTool(t, s, mcptools.NewAPIClient(srv.URL, "k"), "create_api_key", map[string]interface{}{"name": "ci"})
	require.Equal(t, "abc-123", rec.bodies[0]["check_id"])
	require.Equal(t, "ingest", rec.bodies[0]["scope"])
	require.NotContains(t, rec.bodies[1], "check_id")
	require.NotContains(t, rec.bodies[1], "scope")
}

// TestTraceSetupTools_DescribeTheCredentialRules. The descriptions are what
// an agent reads before calling: they must carry the storage rules and the
// scope each tool needs.
func TestTraceSetupTools_DescribeTheCredentialRules(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	tools := s.ListTools()
	gts := tools["get_trace_setup"].Tool.Description
	cik := tools["create_ingest_key"].Tool.Description
	for _, want := range []string{"Requires an API key with the read scope", "Carry the steps out yourself", "Never echo the credential", "never put it in committed code"} {
		require.Contains(t, gts, want)
	}
	for _, want := range []string{"Requires an API key with the write scope", "nothing else", "git-ignored", "never echo it back", "never into committed code",
		"Never use your own LastPing API key"} {
		require.Contains(t, cik, want)
	}
	require.Contains(t, tools["create_api_key"].Tool.Description, "create_ingest_key")
	require.NotContains(t, tools["create_api_key"].Tool.Description, "minting one needs a write or admin key")
}
