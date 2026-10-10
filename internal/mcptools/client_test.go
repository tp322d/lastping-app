package mcptools_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lastping-dev/lastping-app/internal/mcptools"
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

// problemServer answers every request with status and body, verbatim.
func problemServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// listAgentsError drives list_agents against srv and returns the tool error's
// text: the string problemDetail produced, as the agent reads it.
func listAgentsError(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")
	result := callTool(t, s, c, "list_agents", map[string]interface{}{})
	require.True(t, result.IsError, "a refused request must surface as a tool error")
	return extractText(result)
}

// TestProblemDetail_FoldsFix: the problem's `fix` member is the sentence that
// says what to do next (the OAuth scope refusal's "Reconnect LastPing with
// read and write access, or use an API key for this route."). It must reach
// the agent after the detail and the required scope, or the agent learns it
// was refused and nothing about the way out.
func TestProblemDetail_FoldsFix(t *testing.T) {
	t.Parallel()
	srv := problemServer(t, http.StatusForbidden, `{"title":"Forbidden","status":403,"code":"INSUFFICIENT_SCOPE",`+
		`"detail":"this sign-in's access does not allow this request","required_scope":"write",`+
		`"fix":"Reconnect LastPing with read and write access, or use an API key for this route."}`)
	require.Equal(t,
		"Forbidden (HTTP 403): this sign-in's access does not allow this request (required_scope: write)"+
			" Fix: Reconnect LastPing with read and write access, or use an API key for this route.",
		listAgentsError(t, srv))
}

// The companion: without `fix`, the string is exactly what it was before the
// fold existed, so no caller that matches on it sees a change.
func TestProblemDetail_WithoutFixIsUnchanged(t *testing.T) {
	t.Parallel()
	srv := problemServer(t, http.StatusForbidden, `{"title":"Forbidden","status":403,`+
		`"detail":"this API key's scope does not allow this request","required_scope":"admin"}`)
	require.Equal(t,
		"Forbidden (HTTP 403): this API key's scope does not allow this request (required_scope: admin)",
		listAgentsError(t, srv))
}

// A fix the detail already carries is not repeated.
func TestProblemDetail_FixAlreadyInDetailIsNotDoubled(t *testing.T) {
	t.Parallel()
	srv := problemServer(t, http.StatusConflict, `{"title":"Conflict","status":409,`+
		`"detail":"slug taken. Pick another slug.","fix":"Pick another slug."}`)
	require.Equal(t, "Conflict (HTTP 409): slug taken. Pick another slug.", listAgentsError(t, srv))
}

// TestProblemDetail_UndecodableBodySaysWhatHappened: a body that is not a
// problem document (a proxy's HTML page) used to surface as a bare "HTTP
// 502", which the connector directory rejects and an agent cannot act on.
// A 5xx says to retry; a 4xx says the request was refused.
func TestProblemDetail_UndecodableBodySaysWhatHappened(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusBadGateway, "LastPing API returned HTTP 502 with no details; try again in a minute"},
		{http.StatusInternalServerError, "LastPing API returned HTTP 500 with no details; try again in a minute"},
		{http.StatusForbidden, "LastPing API refused the request (HTTP 403)"},
		{http.StatusNotFound, "LastPing API refused the request (HTTP 404)"},
	} {
		srv := problemServer(t, tc.status, `<html><body>Bad Gateway</body></html>`)
		require.Equal(t, tc.want, listAgentsError(t, srv), "status %d", tc.status)
	}
}
