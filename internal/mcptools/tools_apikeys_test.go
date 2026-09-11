package mcptools_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// TestCreateAPIKey_ScopePassedThrough — an explicit scope must reach the API
// verbatim, and the tier the key actually came back with must reach the agent.
// A key minted at a tier the caller did not expect is a credential nobody
// audits until it fails.
func TestCreateAPIKey_ScopePassedThrough(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234","scope":"read","created_at":"2026-07-17T00:00:00Z","key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{
		"name":  "ci",
		"scope": "read",
	})
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

	var sent struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.Equal(t, "read", sent.Scope, "an explicit scope must reach the API verbatim")
	require.Contains(t, extractText(result), "scope=read",
		"the agent must be told which tier the key it just received actually has")
}

// TestCreateAPIKey_OmittedScopeIsNotSent — the tool does NOT invent a default.
// The hosted API's own default is already "write" on every surface, so sending
// one from here would be a second place for that policy to live and drift.
func TestCreateAPIKey_OmittedScopeIsNotSent(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234","scope":"write","created_at":"2026-07-17T00:00:00Z","key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{"name": "ci"})
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	_, present := sent["scope"]
	require.False(t, present, "an omitted scope must not be sent at all — the API applies the default")
	require.Contains(t, extractText(result), "scope=write",
		"the tier the API chose must still be reported back to the agent")
}

// TestCreateAPIKey_ScopeIsAnEnumInTheSchema — the three tiers have to be in the
// input schema, not only in prose. An agent that guesses "readonly" or "rw"
// spends a round trip finding out; the enum is what stops the guess.
func TestCreateAPIKey_ScopeIsAnEnumInTheSchema(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	st, ok := s.ListTools()["create_api_key"]
	require.True(t, ok, "create_api_key is not registered")

	raw, ok := st.Tool.InputSchema.Properties["scope"]
	require.True(t, ok, "create_api_key has no scope parameter")
	schema, ok := raw.(map[string]any)
	require.True(t, ok, "scope has an unexpected schema shape %T", raw)

	require.Equal(t, []string{"read", "write", "admin"}, schema["enum"],
		"scope must advertise exactly the three tiers the API accepts")
	require.NotContains(t, st.Tool.InputSchema.Required, "scope",
		"scope is optional — the API defaults it to write")
}

// TestCreateAPIKey_ScopeCapRefusalNamesTheCeiling — a scope the parent key
// cannot grant comes back as a 400 carrying max_scope. The agent must SEE the
// ceiling, not just that it was refused, or its next attempt is another guess.
// This is also the assertion that the max_scope fold in client.go is wired to
// a real tool result rather than only to the error helper.
func TestCreateAPIKey_ScopeCapRefusalNamesTheCeiling(t *testing.T) {
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"scope may not exceed the creating key's own scope","max_scope":"write"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{
		"name":  "escalate",
		"scope": "admin",
	})
	require.True(t, result.IsError, "a refused scope must surface as a tool error")
	require.Equal(t, 1, calls, "a refused scope must NOT be retried at a lower tier")

	text := extractText(result)
	require.Contains(t, text, "scope may not exceed")
	require.Contains(t, text, "max_scope: write",
		"the ceiling from max_scope must reach the agent, so its next attempt is right")
}

// TestListAPIKeys_ReportsScopeAndLineage — the list DTO in this package is kept
// in sync with the hosted API's key response BY HAND, so a field added there
// and forgotten here silently disappears from the agent's view. scope and
// created_by_key_id are exactly what a caller needs before choosing which key
// to revoke, given that revoking cascades.
func TestListAPIKeys_ReportsScopeAndLineage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"key-2","name":"child","prefix":"lp_child01","created_at":"2026-07-17T00:00:00Z","scope":"read","created_by_key_id":"key-1"}]`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_api_keys", map[string]interface{}{})
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

	out := extractText(result)
	require.Contains(t, out, `"scope": "read"`)
	require.Contains(t, out, `"created_by_key_id": "key-1"`)
}

// TestRevokeAPIKey_DescribesTheCascade — the description is the only thing an
// agent reads before calling a destructive, irreversible tool, and that tool
// now deletes an unbounded number of credentials rather than one. A
// description that still says "an API key" understates what the call does, and
// no golden file catches it: wantTools pins tool NAMES only.
func TestRevokeAPIKey_DescribesTheCascade(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	st, ok := s.ListTools()["revoke_api_key"]
	require.True(t, ok, "revoke_api_key is not registered")
	desc := st.Tool.Description

	require.Contains(t, desc, "every key it created",
		"the description must say the revoke cascades")
	require.Contains(t, desc, "recursively",
		"one level is not what it does, and an agent must not assume it is")
	require.Contains(t, desc, "list_api_keys",
		"an agent needs to be pointed at the tool that shows what hangs off this key")
	require.Contains(t, desc, "cannot be undone")
}

// TestRevokeAPIKey_ReportsHowManyKeysWent — the API answers 204 with no body,
// so X-Revoked-Count is the only place the number exists. An agent that has
// just destroyed four credentials and was told "API key X revoked." has been
// told something true and misleading.
//
// The 1 and absent cases are the positive companions to the cascade case: with
// only the cascade case, a fold that appended the sentence unconditionally
// would pass while telling the agent it destroyed 0 extra keys.
func TestRevokeAPIKey_ReportsHowManyKeysWent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		header     string
		want       string
		wantAbsent string
	}{
		{"cascade", "3", "API key key-1 revoked, along with 2 key(s) it created — 3 in total.", ""},
		{"lone key", "1", "API key key-1 revoked.", "in total"},
		{"header absent", "", "API key key-1 revoked.", "in total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("X-Revoked-Count", tc.header)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			c := mcptools.NewAPIClient(srv.URL, "test-key")
			s := newTestServer(t, "https://ping.lastping.dev")

			result := callTool(t, s, c, "revoke_api_key", map[string]interface{}{"api_key_id": "key-1"})
			require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

			text := extractText(result)
			require.Contains(t, text, tc.want)
			if tc.wantAbsent != "" {
				require.NotContains(t, text, tc.wantAbsent,
					"a single-key revoke must not claim it took others with it")
			}
		})
	}
}

// TestRevokeAPIKey_IgnoresAnUnparseableCount — the header is attacker-adjacent
// only in the sense that it comes off the wire, but a proxy that rewrites it,
// or a future server that sends "many", must not produce a nonsense sentence
// or a panic. Falling back to the plain text is the fail-safe direction.
func TestRevokeAPIKey_IgnoresAnUnparseableCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Revoked-Count", "lots")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "revoke_api_key", map[string]interface{}{"api_key_id": "key-1"})
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))
	require.Equal(t, "API key key-1 revoked.", extractText(result))
}
