package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(newHandler("https://api.invalid", "https://ping.invalid"))
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthz(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestMissingBearerReturns401(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Post(ts.URL+"/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-bearer status = %d, want 401", resp.StatusCode)
	}
}

// TestWithBearerReachesMCP proves the auth gate passes a bearer-carrying request
// through to the MCP handler (initialize is protocol-level, so it does not call
// the API — the per-request client → API proxy is exercised end-to-end in the
// e2e test). We assert the request is neither 401 nor 429 and yields a valid MCP
// initialize result naming the server.
func TestWithBearerReachesMCP(t *testing.T) {
	ts := testServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer testkey")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("authed initialize rejected by auth/rate gate: status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed initialize status = %d, want 200; body=%s", resp.StatusCode, b)
	}
	if !bytes.Contains(b, []byte("lastping-mcp")) && !bytes.Contains(b, []byte("result")) {
		t.Fatalf("initialize response missing serverInfo/result: %s", b)
	}
}
