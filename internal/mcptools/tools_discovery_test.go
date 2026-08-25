package mcptools_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// discover_monitors_reconcile is a pure proxy to POST
// /api/v1/discovery/reconcile. It owns no rule about what a valid source is —
// the API does — so these tests exercise only what the proxy itself is
// responsible for: forwarding the scan without reshaping it, refusing the one
// input it must not forward, and rendering the rejection's `fix` field.

// TestDiscoverMonitorsReconcile_ForwardsSourcesRaw is the guard against the
// re-marshalling failure mode: the tool decodes entries as json.RawMessage, so
// a field the API's source shape grows tomorrow reaches it untouched today. A
// struct-shaped proxy would silently drop everything it does not name, which is
// exactly how a field went missing on the response side once before.
func TestDiscoverMonitorsReconcile_ForwardsSourcesRaw(t *testing.T) {
	var capturedBody []byte
	var capturedMethod, capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod, capturedPath = r.Method, r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":[{"id":"a"}],"existing":[],"orphaned":[{"id":"b"}]}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	// a_field_this_client_has_never_heard_of stands in for whatever the API
	// adds next; it must survive the hop.
	result := callTool(t, s, c, "discover_monitors_reconcile", map[string]interface{}{
		"sources": `[{"source_kind":"crontab","source_ref":"/etc/cron.d/backup:/usr/local/bin/backup.sh",` +
			`"schedule_cron":"0 3 * * *","tz":"Europe/Berlin","a_field_this_client_has_never_heard_of":{"nested":true}}]`,
	})
	require.False(t, result.IsError, "expected success: %s", resultText(t, result))

	assert.Equal(t, "POST", capturedMethod)
	assert.Equal(t, "/api/v1/discovery/reconcile", capturedPath)

	var sent struct {
		Sources []map[string]interface{} `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.Len(t, sent.Sources, 1)
	assert.Contains(t, sent.Sources[0], "a_field_this_client_has_never_heard_of",
		"an unknown field was dropped: entries must be forwarded raw, not decoded into a local struct")

	// The summary states what was NOT done, because that is the half an agent
	// is most likely to assume wrongly.
	text := resultText(t, result)
	assert.Contains(t, text, "1 created, 0 already monitored, 1 orphaned")
	assert.Contains(t, text, "Nothing was deleted, paused or modified")
}

// TestDiscoverMonitorsReconcile_EmptyScanIsForwarded: `[]` is a scanner
// reporting it found nothing, which is a legitimate drift report, not an error.
func TestDiscoverMonitorsReconcile_EmptyScanIsForwarded(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":[],"existing":[],"orphaned":[]}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "discover_monitors_reconcile", map[string]interface{}{"sources": "[]"})
	require.False(t, result.IsError, "an empty scan is a valid report, not an error")
	assert.JSONEq(t, `{"sources":[]}`, string(capturedBody))
}

// TestDiscoverMonitorsReconcile_RejectsNull: a JSON null cannot be told apart
// from a caller that produced nothing at all, and forwarding it would hand that
// caller a drift report claiming its whole fleet is dead. It is refused one hop
// before the API, where the message can name the argument the agent passed.
func TestDiscoverMonitorsReconcile_RejectsNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request must not reach the API")
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	for _, bad := range []string{"null", `{"source_kind":"crontab"}`, "not json"} {
		result := callTool(t, s, c, "discover_monitors_reconcile", map[string]interface{}{"sources": bad})
		require.True(t, result.IsError, "expected %q to be refused", bad)
		assert.Contains(t, resultText(t, result), "sources must be a JSON array")
	}
}

// TestDiscoverMonitorsReconcile_RejectionCarriesTheFix is why this file does not
// use c.problem: c.problem reads title/status/detail only, and for this endpoint
// the remedy IS the message. An agent told "tz is required" and nothing else has
// the rule without the means to satisfy it, and its next move is to invent
// "UTC" — the single failure this endpoint cannot detect.
func TestDiscoverMonitorsReconcile_RejectionCarriesTheFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Invalid request","status":400,"detail":"tz is required for a crontab source",` +
			`"code":"TIMEZONE_REQUIRED","fix":"Read the host's zone with: timedatectl show -p Timezone --value"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "discover_monitors_reconcile", map[string]interface{}{
		"sources": `[{"source_kind":"crontab","source_ref":"x","schedule_cron":"0 3 * * *"}]`,
	})
	require.True(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "TIMEZONE_REQUIRED", "the machine-matchable code must survive")
	assert.Contains(t, text, "tz is required for a crontab source")
	assert.Contains(t, text, "timedatectl show -p Timezone --value",
		"the `fix` field is the whole point of not using c.problem here")
}

// TestDiscoverMonitorsReconcile_NonProblemBodyFallsBackToStatus: a proxy's HTML
// error page must not be rendered as an empty rejection.
func TestDiscoverMonitorsReconcile_NonProblemBodyFallsBackToStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "discover_monitors_reconcile", map[string]interface{}{"sources": "[]"})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "HTTP 502")
}
