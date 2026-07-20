// Command lastping-mcp-server serves LastPing's MCP tools over remote
// Streamable HTTP (at mcp.lastping.dev in production).
//
// Unlike the stdio `lastping-mcp` binary — single tenant, API key from the
// environment — this server is multi-tenant: each request carries its own
// LastPing API key as an `Authorization: Bearer` token, and the tool handlers
// resolve a per-request API client from it (via internal/mcptools). The server
// holds no business logic; it is a thin transport + auth + rate-limit layer that
// proxies to the LastPing API.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

const (
	defaultAPIBase  = "https://app.lastping.dev"
	defaultPingHost = "https://ping.lastping.dev"
	defaultPort     = "8080"
	version         = "0.1.0"

	// bearerRatePerMin caps requests per API key per minute — a generous ceiling
	// for interactive agent use; abusive callers get 429 + Retry-After.
	bearerRatePerMin = 120
	rateLimitSlots   = 4096
)

func main() {
	// `-healthcheck` dials the local /healthz and exits 0/1 — used by the
	// container healthcheck since the distroless image has no shell or curl.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		resp, err := http.Get("http://127.0.0.1:" + envOr("LP_MCP_PORT", defaultPort) + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		_ = resp.Body.Close()
		return
	}

	apiBase := envOr("LP_API_BASE", defaultAPIBase)
	pingHost := envOr("LP_PING_HOST", defaultPingHost)
	port := envOr("LP_MCP_PORT", defaultPort)

	slog.Info("lastping-mcp-server starting",
		"port", port, "api_base", apiBase, "service_version", os.Getenv("LP_SERVICE_VERSION"))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(apiBase, pingHost),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// newHandler builds the full HTTP handler: /healthz (open) and the MCP endpoint
// at root behind bearer auth + per-token rate limiting. Split out from main so
// tests can drive it with httptest.
func newHandler(apiBase, pingHost string) http.Handler {
	s := server.NewMCPServer(
		"lastping-mcp",
		version,
		server.WithToolCapabilities(true),
	)
	mcptools.Register(s, pingHost)

	// Stateless: every request is self-contained and carries its own bearer
	// token. WithHTTPContextFunc turns that token into a per-request API client.
	streamable := server.NewStreamableHTTPServer(s,
		server.WithStateLess(true),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if key := bearerToken(r); key != "" {
				return mcptools.ContextWithClient(ctx, mcptools.NewAPIClient(apiBase, key))
			}
			return ctx
		}),
	)

	limiter := newTokenRateLimiter(rateLimitSlots, bearerRatePerMin, time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", bearerAuth(limiter, streamable))
	return mux
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// bearerToken extracts a non-empty bearer token from the Authorization header,
// or "" if absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// bearerAuth rejects requests without a bearer token (401) and rate-limits by a
// hash of the token (429) before delegating to the MCP handler. Returning 401 at
// the HTTP layer gives callers a clean error instead of a confusing tool result.
func bearerAuth(limiter *tokenRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid Authorization bearer token")
			return
		}
		if !limiter.Allow(hashToken(token)) {
			w.Header().Set("Retry-After", "60")
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded; retry after 60s")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
