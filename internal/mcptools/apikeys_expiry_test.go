package mcptools_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// TestCreateAPIKey_OmittedExpiryDefaultsTo90Days — REST alone still treats an
// omitted expires_at as "never", but
// that contract only makes sense for a caller who typed the request by hand.
// An agent calling create_api_key almost never sets expires_at at all, so
// leaving the default to REST would mint permanent credentials by omission
// on the one surface that is hardest for a human to notice. The MCP tool
// closes that gap itself: an omitted expires_at becomes now+90d before the
// HTTP request is even built.
func TestCreateAPIKey_OmittedExpiryDefaultsTo90Days(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234","created_at":"2026-07-17T00:00:00Z","key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	before := time.Now().UTC().Add(90 * 24 * time.Hour)
	result := callTool(t, s, c, "create_api_key", map[string]interface{}{"name": "ci"})
	after := time.Now().UTC().Add(90 * 24 * time.Hour)
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

	var sent struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.NotEmpty(t, sent.ExpiresAt, "omitted expires_at must default to a 90-day expiry, not be sent absent")

	got, err := time.Parse(time.RFC3339, sent.ExpiresAt)
	require.NoError(t, err, "expires_at must be RFC 3339: %q", sent.ExpiresAt)
	require.WithinRange(t, got, before.Add(-time.Minute), after.Add(time.Minute),
		"expected expires_at to be ~90 days from now, got %s", sent.ExpiresAt)
	require.True(t, strings.HasSuffix(sent.ExpiresAt, "Z"), "expected UTC (Z-suffixed) RFC 3339, got %q", sent.ExpiresAt)
}

// TestCreateAPIKey_ExplicitExpiryPassedThrough — an agent that supplies an
// explicit expires_at is trusted verbatim; the 90-day default only fills in
// for the omitted case.
func TestCreateAPIKey_ExplicitExpiryPassedThrough(t *testing.T) {
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234","created_at":"2026-07-17T00:00:00Z","key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{
		"name":       "ci",
		"expires_at": "2099-01-01T00:00:00Z",
	})
	require.False(t, result.IsError, "unexpected tool error: %s", extractText(result))

	var sent struct {
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.Equal(t, "2099-01-01T00:00:00Z", sent.ExpiresAt)
}

// TestCreateAPIKey_RetriesAtTheCeilingWhenTheParentCapsIt — the 90-day default
// above is not a value the server always accepts. A key minted by a key can
// never outlive it, and the calling key almost always HAS
// an expiry: console keys default to 90 days, connect-page keys default to 90
// days, and a key minted by this very tool defaults to 90 days. So "now + 90
// days" is later than the parent's own expiry from the parent's first second
// onward, and sending it unconditionally would have made create_api_key fail
// for essentially every agent that ever called it.
//
// The refusal carries max_expires_at — the latest expiry this caller could
// have asked for — so the retry asks for exactly that. It must NOT simply drop
// expires_at: an omitted value means "never" on a server with expiry
// inheritance switched off, which is the opposite of what an agent
// holding a one-hour key should be handed.
func TestCreateAPIKey_RetriesAtTheCeilingWhenTheParentCapsIt(t *testing.T) {
	const ceiling = "2026-07-17T01:00:00Z"
	var bodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,` +
				`"detail":"expires_at may not exceed the creating key's own expiry",` +
				`"max_expires_at":"` + ceiling + `"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234",` +
			`"created_at":"2026-07-17T00:00:00Z","expires_at":"` + ceiling + `",` +
			`"key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{"name": "ci"})
	require.False(t, result.IsError, "the retry must succeed, not surface the 400: %s", extractText(result))
	require.Contains(t, extractText(result), "lp_abcd1234secret", "the plaintext key from the retry must reach the agent")

	require.Len(t, bodies, 2, "expected exactly one retry, got %d request(s)", len(bodies))

	var first map[string]any
	require.NoError(t, json.Unmarshal(bodies[0], &first))
	require.NotEmpty(t, first["expires_at"], "the FIRST attempt must still carry the 90-day default")
	require.NotEqual(t, ceiling, first["expires_at"], "the first attempt is the default, not the ceiling")

	var second map[string]any
	require.NoError(t, json.Unmarshal(bodies[1], &second))
	require.Equal(t, ceiling, second["expires_at"],
		"the retry must ask for exactly the ceiling the refusal named, got: %s", bodies[1])
	require.Equal(t, "ci", second["name"], "the retry must ask for the same key, not a differently named one")
}

// TestCreateAPIKey_RetryOmitsExpiryWhenNoCeilingIsGiven — the fallback. A
// refusal that names no max_expires_at leaves the tool nothing to ask for, so
// it omits expires_at and lets the server decide. Worth a test of its own
// because it is the branch that runs against any server older than the
// extension member.
func TestCreateAPIKey_RetryOmitsExpiryWhenNoCeilingIsGiven(t *testing.T) {
	var bodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,` +
				`"detail":"expires_at may not exceed the creating key's own expiry"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"key-1","name":"ci","prefix":"lp_abcd1234",` +
			`"created_at":"2026-07-17T00:00:00Z","key":"lp_abcd1234secret"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{"name": "ci"})
	require.False(t, result.IsError, "the fallback retry must succeed: %s", extractText(result))
	require.Len(t, bodies, 2, "expected exactly one retry, got %d request(s)", len(bodies))

	var second map[string]any
	require.NoError(t, json.Unmarshal(bodies[1], &second))
	_, hasExpiry := second["expires_at"]
	require.False(t, hasExpiry,
		"with no ceiling to ask for, the retry must omit expires_at, got: %s", bodies[1])
}

// TestCreateAPIKey_ExplicitExpiryIsNotRetried — the retry exists to recover a
// default this tool chose, never to quietly rewrite what the agent asked for.
// An agent that named an expiry and was refused must SEE the refusal: silently
// substituting a different lifetime for the one it requested would be the tool
// editing its caller's intent.
func TestCreateAPIKey_ExplicitExpiryIsNotRetried(t *testing.T) {
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,` +
			`"detail":"expires_at may not exceed the creating key's own expiry"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{
		"name":       "ci",
		"expires_at": "2099-01-01T00:00:00Z",
	})
	require.True(t, result.IsError, "an explicitly requested expiry that the server refuses must surface as an error")
	require.Contains(t, extractText(result), "may not exceed the creating key's own expiry")
	require.Equal(t, 1, requests, "an explicit expires_at must never be retried")
}

// TestCreateAPIKey_OtherBadRequestIsNotRetried — the retry is keyed on ONE
// exact problem detail. Any other 400 (a missing name, an expiry already in
// the past) means something else is wrong, and re-sending the same request
// minus its expiry would turn a clear error into a second, more confusing one.
func TestCreateAPIKey_OtherBadRequestIsNotRetried(t *testing.T) {
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"name is required"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "create_api_key", map[string]interface{}{"name": "ci"})
	require.True(t, result.IsError, "an unrelated 400 must reach the agent")
	require.Contains(t, extractText(result), "name is required")
	require.Equal(t, 1, requests, "only the parent-expiry cap is retried")
}

// TestCreateAPIKey_ScopePassedThrough — an argument the tool declares but drops
// is worse than an absent one, because the agent is told it succeeded at a tier
// it did not get. This pins what actually reaches the API.
