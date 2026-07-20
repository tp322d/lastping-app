package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/tp322d/lastping-app/internal/mcptools"
)

const (
	defaultBaseURL  = "https://app.lastping.dev"
	defaultPingHost = "https://ping.lastping.dev"
	version         = "0.1.0"
)

func main() {
	apiKey := os.Getenv("LASTPING_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: LASTPING_API_KEY environment variable is not set.\n"+
			"Obtain your API key from https://app.lastping.dev (Project Settings → API Keys).")
		os.Exit(1)
	}

	baseURL := os.Getenv("LASTPING_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	pingHost := os.Getenv("LASTPING_PING_HOST")
	if pingHost == "" {
		pingHost = defaultPingHost
	}

	envClient := mcptools.NewAPIClient(baseURL, apiKey)

	s := server.NewMCPServer(
		"lastping-mcp",
		version,
		server.WithToolCapabilities(true),
	)

	mcptools.Register(s, pingHost)

	// WithStdioContextFunc seeds the *APIClient into every request context.
	// The stdio server calls this once and shares the resulting context for all
	// requests — exactly right for a single-tenant binary that reads credentials
	// from environment variables at startup.
	if err := server.ServeStdio(s, server.WithStdioContextFunc(func(ctx context.Context) context.Context {
		return mcptools.ContextWithClient(ctx, envClient)
	})); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
