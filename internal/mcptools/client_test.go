package mcptools_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// TestAPIClient_InsufficientScopeNamesTheRequiredScope — a 403 from a scoped
// route carries required_scope naming the tier the route needed. The
// agent-visible error must say WHICH scope it needs, not just that it was
// refused: an agent holding a write key has no way to know it needs an admin
// key instead, and a 403 with no anticipated cause looks like a transient
// fault, so it retries.
func TestAPIClient_InsufficientScopeNamesTheRequiredScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Forbidden","status":403,"detail":"this API key's scope does not allow this request","code":"INSUFFICIENT_SCOPE","fix":"Use an API key with the admin scope, or re-mint this key with that scope from Settings.","required_scope":"admin"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_agents", map[string]interface{}{})
	require.True(t, result.IsError, "a 403 must surface as a tool error")

	text := extractText(result)
	require.Contains(t, text, "this API key's scope does not allow this request")
	require.Contains(t, text, "(required_scope: admin)",
		"the tier the route needed must reach the agent, so its next attempt is right")
}

// TestAPIClient_ProblemWithoutScopeFieldsIsUnchanged is the positive companion
// to the two fold tests. Without it, both of those would still pass if the
// fold appended something to EVERY problem — the folds must apply only when
// the API actually sent the member.
func TestAPIClient_ProblemWithoutScopeFieldsIsUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"slug already in use"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_agents", map[string]interface{}{})
	require.True(t, result.IsError)

	text := extractText(result)
	require.Equal(t, "Bad Request (HTTP 400): slug already in use", text,
		"a problem carrying neither scope member must be rendered exactly as before")
	require.NotContains(t, text, "scope:")
}

// TestAPIClient_MaxScopeIsFoldedIntoTheError covers the other half of the pair
// at the client level, so the fold is pinned independently of create_api_key's
// own path through it.
func TestAPIClient_MaxScopeIsFoldedIntoTheError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"scope may not exceed the creating key's own scope","max_scope":"read"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_agents", map[string]interface{}{})
	require.True(t, result.IsError)
	require.Equal(t,
		"Bad Request (HTTP 400): scope may not exceed the creating key's own scope (max_scope: read)",
		extractText(result))
}
