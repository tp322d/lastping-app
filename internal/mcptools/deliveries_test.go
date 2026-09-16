package mcptools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

// TestListDeliveries verifies list_deliveries proxies to GET
// /api/v1/deliveries and returns the DeliveryPage JSON wrapped in the
// untrusted-output envelope with the exact field set deliveries.go declares.
func TestListDeliveries(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deliveries": [
				{
					"id": 91234, "check_id": "abc-123", "check_name": "nightly ETL",
					"incident_opened_at": "2026-07-10T03:10:00Z", "incident_id": 4821,
					"event_type": "fail", "channel_id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
					"channel_name": "ops-pagerduty", "channel_kind": "webhook",
					"status": "delivered", "attempts": 1,
					"delivered_at": "2026-07-10T03:10:05Z", "last_error": ""
				}
			],
			"next_cursor": null
		}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_deliveries", map[string]interface{}{"monitor": "abc-123", "status": "delivered"})
	assert.False(t, result.IsError, "expected success; got: %s", extractText(result))
	assert.Contains(t, capturedPath, "/api/v1/deliveries")
	assert.Contains(t, capturedPath, "monitor=abc-123")
	assert.Contains(t, capturedPath, "status=delivered")

	var env struct {
		UntrustedFields []string        `json:"untrusted_fields"`
		Data            json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(result)), &env))
	require.Equal(t, []string{"deliveries.last_error", "deliveries.channel_name", "deliveries.check_name"}, env.UntrustedFields)

	var data struct {
		Deliveries []struct {
			Status string `json:"status"`
		} `json:"deliveries"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Len(t, data.Deliveries, 1)
	assert.Equal(t, "delivered", data.Deliveries[0].Status)
}

// TestListDeliveries_InvalidFilter verifies a 400 refusal from GET
// /api/v1/deliveries becomes a tool error carrying the API's own detail
// text.
func TestListDeliveries_InvalidFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"status must be one of pending, delivered, dead, suppressed"}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_deliveries", map[string]interface{}{"status": "bogus"})
	assert.True(t, result.IsError, "expected error result for 400")
	assert.Contains(t, extractText(result), "status must be one of pending, delivered, dead, suppressed")
}

// TestListDeliveries_Empty verifies an empty page produces a plain-text
// result rather than an empty envelope.
func TestListDeliveries_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deliveries": [], "next_cursor": null}`))
	}))
	defer srv.Close()

	c := mcptools.NewAPIClient(srv.URL, "test-key")
	s := newTestServer(t, "https://ping.lastping.dev")

	result := callTool(t, s, c, "list_deliveries", map[string]interface{}{})
	assert.False(t, result.IsError)
	assert.Contains(t, extractText(result), "No deliveries found")
}

// TestListDeliveries_GoldenIncludesTool verifies list_deliveries is present
// in the registered tool surface.
func TestListDeliveries_GoldenIncludesTool(t *testing.T) {
	s := newTestServer(t, "https://ping.lastping.dev")
	_, ok := s.ListTools()["list_deliveries"]
	require.True(t, ok, "list_deliveries must be registered")
}
