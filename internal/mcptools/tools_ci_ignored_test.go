package mcptools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// get_monitor decodes the REST read into a typed struct, so a REST field the
// struct lacks is silently dropped. ci_ignored must survive that, and its two
// payload-supplied names must be flagged as job data.
const (
	ciIgnoredCheckJSON = `{"id":"ci-1","name":"Build","slug":"build","status":"up","ping_url":"https://ping.lastping.dev/ci-1","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-22T00:00:00Z","tags":[],"ci_provider":"github","ci_configured":true,"ci_branch":"main","ci_ignored":{"count":12,"last_at":"2026-10-08T14:02:11Z","last_workflow":"CI","last_branch":"feature/x","last_matched_at":"2026-10-01T09:00:00Z","filters_changed_at":null,"same_workflow_count":4,"same_workflow_last_at":"2026-10-08T11:40:00Z","same_workflow_last_branch":"release/x","new_workflow":"Deploy prod","new_workflow_first_at":"2026-10-08T12:00:00Z","new_workflow_count":3}}`
	ciPlainCheckJSON   = `{"id":"ci-2","name":"Build","slug":"build2","status":"up","ping_url":"https://ping.lastping.dev/ci-2","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-22T00:00:00Z","tags":[],"ci_provider":"github","ci_configured":true}`
)

func ciIgnoredGetMonitor(t *testing.T, id string) string {
	t.Helper()
	return ciIgnoredCall(t, "get_monitor", map[string]interface{}{"id": id})
}

// ciIgnoredCall runs one tool against a fake API whose every monitor read
// (GET, list, PATCH, reconcile) carries the ci_ignored object on ci-1.
func ciIgnoredCall(t *testing.T, tool string, args map[string]interface{}) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/checks/ci-1":
			_, _ = w.Write([]byte(ciIgnoredCheckJSON))
		case "/api/v1/checks/ci-2":
			_, _ = w.Write([]byte(ciPlainCheckJSON))
		case "/api/v1/checks":
			_, _ = w.Write([]byte("[" + ciIgnoredCheckJSON + "," + ciPlainCheckJSON + "]"))
		case "/api/v1/discovery/reconcile":
			_, _ = w.Write([]byte(`{"created":[],"existing":[` + ciIgnoredCheckJSON + `],"orphaned":[` + ciPlainCheckJSON + `]}`))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	t.Cleanup(srv.Close)
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")
	result := callTool(t, s, c, tool, args)
	require.False(t, result.IsError, extractText(result))
	return extractText(result)
}

func TestGetMonitor_PassesCIIgnoredThroughAndFlagsItsNames(t *testing.T) {
	text := ciIgnoredGetMonitor(t, "ci-1")
	var got map[string]interface{}
	require.NoError(t, json.NewDecoder(strings.NewReader(text)).Decode(&got))
	ign, ok := got["ci_ignored"].(map[string]interface{})
	require.True(t, ok, "ci_ignored must survive the typed decode: %s", text)
	assert.EqualValues(t, 12, ign["count"])
	assert.Equal(t, "2026-10-08T14:02:11Z", ign["last_at"])
	assert.Equal(t, "CI", ign["last_workflow"])
	assert.Equal(t, "feature/x", ign["last_branch"])
	assert.Equal(t, "2026-10-01T09:00:00Z", ign["last_matched_at"])
	v, present := ign["filters_changed_at"]
	assert.True(t, present, "filters_changed_at is printed even when null")
	assert.Nil(t, v)
	assert.EqualValues(t, 4, ign["same_workflow_count"])
	assert.Equal(t, "2026-10-08T11:40:00Z", ign["same_workflow_last_at"])
	assert.Equal(t, "release/x", ign["same_workflow_last_branch"])
	assert.Equal(t, "Deploy prod", ign["new_workflow"])
	assert.Equal(t, "2026-10-08T12:00:00Z", ign["new_workflow_first_at"])
	assert.EqualValues(t, 3, ign["new_workflow_count"])
	assert.Contains(t, text, "ci_ignored.last_workflow, ci_ignored.last_branch, ci_ignored.same_workflow_last_branch and ci_ignored.new_workflow are raw output")
}

func TestGetMonitor_NoCIIgnored_NoNote(t *testing.T) {
	// Companion: the note is tied to the object, not printed on every read.
	text := ciIgnoredGetMonitor(t, "ci-2")
	assert.NotContains(t, text, "ci_ignored")
}

// The surfaces other than get_monitor print no untrusted-data note, so they
// must not print the payload-supplied names at all. Each test reads the same
// fake API as the get_monitor test above, whose ci-1 carries ci_ignored, so
// an absence here is the tool dropping it, not the fixture lacking it.
func TestListMonitors_OmitsCIIgnored(t *testing.T) {
	text := ciIgnoredCall(t, "list_monitors", map[string]interface{}{})
	assert.Contains(t, text, `"ci-1"`, "the monitor itself is listed")
	assert.NotContains(t, text, "ci_ignored")
	assert.NotContains(t, text, "feature/x")
	assert.NotContains(t, text, "release/x")
}

func TestUpdateMonitor_OmitsCIIgnored(t *testing.T) {
	text := ciIgnoredCall(t, "update_monitor", map[string]interface{}{"id": "ci-1", "name": "Renamed"})
	assert.Contains(t, text, "Monitor updated")
	assert.Contains(t, text, `"ci-1"`)
	assert.NotContains(t, text, "ci_ignored")
	assert.NotContains(t, text, "feature/x")
	assert.NotContains(t, text, "release/x")
}

func TestDiscoverMonitorsReconcile_StripsCIIgnored(t *testing.T) {
	text := ciIgnoredCall(t, "discover_monitors_reconcile", map[string]interface{}{"sources": `[]`})
	assert.Contains(t, text, "1 already monitored")
	assert.Contains(t, text, `"ci-1"`, "the monitor itself is forwarded")
	assert.Contains(t, text, `"ci_branch": "main"`, "the rest of the monitor DTO survives the rewrite")
	assert.NotContains(t, text, "ci_ignored")
	assert.NotContains(t, text, "feature/x")
	assert.NotContains(t, text, "release/x")
}
