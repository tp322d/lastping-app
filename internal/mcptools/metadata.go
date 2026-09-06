package mcptools

// metadata.go — the server's own identity, and the one place both binaries
// are built.
//
// WHAT WAS MISSING
//
// initialize returned two fields:
//
//	"serverInfo": {"name": "lastping-mcp", "version": "0.1.0"}
//
// No title, no description, no website, no icon. A client listing available
// MCP servers had a bare name to show a person, and nothing to render beside
// it. The hosted server at mcp.lastping.dev was corrected first; this is the
// same fix for the binary people actually install.
//
// WHY BOTH BINARIES NOW SHARE A CONSTRUCTOR
//
// cmd/lastping-mcp (stdio) and cmd/lastping-mcp-server (streamable HTTP) each
// built their own server with an identical NewMCPServer call. Identical by
// coincidence, not by construction: nothing stopped them drifting, and adding
// four metadata options to both by hand would have been four more chances for
// exactly that. NewServer below is the only place either binary builds one, so
// the stdio and remote transports cannot describe the same product
// differently.
//
// It also makes the metadata testable. Both call sites are inside package
// main, which a test cannot easily reach; a constructor in this package can be
// driven directly, which is what metadata_test.go does.

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Metadata this server reports about itself.
//
// These values match server.json in the private monorepo, which is the
// manifest published to registry.modelcontextprotocol.io, and the constants
// the hosted server reports. Three copies of four facts is not ideal; the
// monorepo asserts its pair against each other in a test, and this copy is
// pinned by TestMetadata_MatchesPublishedIdentity below so a change here is at
// least deliberate rather than accidental.
const (
	// ServerName is the transport-level implementation name — what a client
	// prints in a connection list. Deliberately not the registry identifier
	// (dev.lastping/lastping), which is a different thing in a different file.
	ServerName = "lastping-mcp"

	// Title is the human-readable display name.
	Title = "LastPing"

	// Description is one sentence, matching the registry manifest exactly.
	// The em dash is intentional.
	Description = "Monitoring that agents set up for themselves — cron jobs, CI/CD pipelines and AI agent runs."

	// WebsiteURL points at /agents/ rather than the apex: someone reaching for
	// a server's website from inside an MCP client wants the page about using
	// it with an agent.
	WebsiteURL = "https://lastping.dev/agents/"

	// IconURL is the 512px app icon served by the marketing site. A remote
	// HTTPS URL rather than a data URI — inlining 18 KB would put it on every
	// initialize response.
	IconURL      = "https://lastping.dev/icon-512.png"
	IconMIMEType = "image/png"
	IconSize     = "512x512"
)

// NewServer builds the MCP server both binaries serve, with every tool
// registered and the server's identity attached.
//
// version is passed in because it is a build-time value each command sets for
// itself (goreleaser stamps the released binaries); everything else about the
// server's identity is the same wherever it runs, so it lives here.
//
// pingHost is the base URL for ping endpoints, forwarded to Register.
func NewServer(version, pingHost string) *server.MCPServer {
	s := server.NewMCPServer(
		ServerName,
		version,
		server.WithToolCapabilities(true),
		server.WithTitle(Title),
		server.WithDescription(Description),
		server.WithWebsiteURL(WebsiteURL),
		server.WithIcons(mcp.Icon{
			Src:      IconURL,
			MIMEType: IconMIMEType,
			Sizes:    []string{IconSize},
		}),
	)
	Register(s, pingHost)
	return s
}
