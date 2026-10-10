package mcptools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lastping-dev/lastping-app/internal/mcptools"
)

const (
	discoveredCheckJSON = `{"id":"d-1","name":"Nightly","slug":"nightly","status":"up","ping_url":"https://ping.lastping.dev/d-1","schedule_kind":"simple","period_s":86400,"grace_s":300,"paused":false,"created_at":"2026-07-22T00:00:00Z","tags":[],"source_kind":"github-actions","source_ref":".github/workflows/nightly.yml"}`
	humanCheckJSON      = `{"id":"h-1","name":"Backup","slug":"backup","status":"up","ping_url":"https://ping.lastping.dev/h-1","schedule_kind":"simple","period_s":3600,"grace_s":300,"paused":false,"created_at":"2026-07-22T00:00:00Z","tags":[]}`
)

// sourceFieldsFakeAPI answers the monitor reads the way the REST API does: the
// discovered monitor carries source_kind and source_ref, the human-made one
// carries neither key. Sub-resource reads (assertions, guards, routes) get an
// empty set.
func sourceFieldsFakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/checks":
			_, _ = w.Write([]byte("[" + discoveredCheckJSON + "," + humanCheckJSON + "]"))
		case "/api/v1/checks/d-1":
			_, _ = w.Write([]byte(discoveredCheckJSON))
		case "/api/v1/checks/h-1":
			_, _ = w.Write([]byte(humanCheckJSON))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListMonitors_ReturnsSourceFields(t *testing.T) {
	srv := sourceFieldsFakeAPI(t)
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_monitors", map[string]interface{}{})
	require.False(t, result.IsError, extractText(result))

	var got []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(extractText(result)), &got))
	require.Len(t, got, 2)

	assert.Equal(t, "d-1", got[0]["id"])
	assert.Equal(t, "h-1", got[1]["id"])

	assert.Equal(t, "github-actions", got[0]["source_kind"])
	assert.Equal(t, ".github/workflows/nightly.yml", got[0]["source_ref"])

	assert.NotContains(t, got[1], "source_kind", "a human-made monitor has no source")
	assert.NotContains(t, got[1], "source_ref", "a human-made monitor has no source")
}

// getMonitorFirstValue decodes the first JSON value of a get_monitor result.
// The stub's default branch answers the assertions, guards and routes reads,
// and only the first value is decoded because get_monitor appends notes after it.
func getMonitorFirstValue(t *testing.T, id string) map[string]interface{} {
	t.Helper()
	srv := sourceFieldsFakeAPI(t)
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "get_monitor", map[string]interface{}{"id": id})
	require.False(t, result.IsError, extractText(result))

	var got map[string]interface{}
	require.NoError(t, json.NewDecoder(strings.NewReader(extractText(result))).Decode(&got))
	return got
}

func TestGetMonitor_ReturnsSourceFields(t *testing.T) {
	got := getMonitorFirstValue(t, "d-1")
	assert.Equal(t, "github-actions", got["source_kind"])
	assert.Equal(t, ".github/workflows/nightly.yml", got["source_ref"])
}

func TestGetMonitor_HumanMadeHasNoSourceFields(t *testing.T) {
	got := getMonitorFirstValue(t, "h-1")
	assert.Equal(t, "h-1", got["id"])
	assert.NotContains(t, got, "source_kind")
	assert.NotContains(t, got, "source_ref")
}
