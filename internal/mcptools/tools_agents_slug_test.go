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

// TestUpdateAgent_SlugSentOnlyWhenPassed: a slug argument reaches the PATCH
// body and the returned agent carries it; a name-only call sends no slug key
// at all (the API then keeps the stored slug), and neither does an empty or
// whitespace-only slug, which means "not asked". This matches the hosted
// server at mcp.lastping.dev.
func TestUpdateAgent_SlugSentOnlyWhenPassed(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(capturedBody, &in)
		slug := "codex"
		if v, ok := in["slug"].(string); ok {
			slug = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent-1","slug":"` + slug + `","name":"Reddit Bot","description":"","status":"up","monitor_count":0,"created_at":"2026-07-17T00:00:00Z"}`))
	}))
	defer srv.Close()
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "update_agent", map[string]interface{}{"id": "agent-1", "name": "Reddit Bot", "slug": "reddit-bot"})
	require.False(t, result.IsError, extractText(result))
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "reddit-bot", body["slug"], "a passed slug is sent")
	assert.Contains(t, extractText(result), `"slug": "reddit-bot"`)

	for _, args := range []map[string]interface{}{
		{"id": "agent-1", "name": "Reddit Bot"},
		{"id": "agent-1", "name": "Reddit Bot", "slug": ""},
		{"id": "agent-1", "name": "Reddit Bot", "slug": "   "},
	} {
		result = callTool(t, s, c, "update_agent", args)
		require.False(t, result.IsError, extractText(result))
		body = nil
		require.NoError(t, json.Unmarshal(capturedBody, &body))
		assert.Equal(t, "Reddit Bot", body["name"])
		_, hasSlug := body["slug"]
		assert.False(t, hasSlug, "no slug key unless one was asked for: %v", args)
	}
}

// TestUpdateAgent_DescriptionStatesTheSlugEffects pins the parts of
// update_agent's description an agent needs before it touches a slug: it
// changes only when asked, and what stops matching the old one.
func TestUpdateAgent_DescriptionStatesTheSlugEffects(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	tool := s.GetTool("update_agent")
	require.NotNil(t, tool)
	d := tool.Tool.Description
	for _, want := range []string{
		"Renaming never changes the slug",
		"only when the person asks",
		"saved links, Terraform references and trace sources (service.name) that name the old slug stop matching this agent, " +
			"unless they also equal its name (case-insensitive)",
		// Attribution is resolved when runs are read, and a slug outranks a
		// name match or an adoption.
		"That reaches back: past traced runs from the old slug's source on monitors this agent does not own lose this agent",
		"A slug also outranks another agent's name match or adopted source",
		"takes that source's traces, past runs included",
	} {
		assert.Contains(t, d, want)
	}
	assert.NotContains(t, d, "immutable")
	props := tool.Tool.InputSchema.Properties
	require.Contains(t, props, "slug")
	assert.NotContains(t, tool.Tool.InputSchema.Required, "slug")
}
