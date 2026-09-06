package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// serverInfo drives a real initialize through the server and returns the
// serverInfo a client receives.
//
// Reading it off the protocol response, not off the struct, is the point: the
// only thing that matters is what a client is told.
func serverInfo(t *testing.T, version string) map[string]any {
	t.Helper()

	s := NewServer(version, "https://ping.example.test")
	const req = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

	raw, err := json.Marshal(s.HandleMessage(context.Background(), []byte(req)))
	require.NoError(t, err, "marshal initialize response")

	var env struct {
		Result struct {
			ServerInfo map[string]any `json:"serverInfo"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &env), "parse initialize response: %s", raw)
	require.NotNil(t, env.Result.ServerInfo, "initialize returned no serverInfo: %s", raw)
	return env.Result.ServerInfo
}

// TestMetadata_ServerReportsItsIdentity is the test that would have failed
// before this change.
//
// It builds the server the binaries actually serve and reads the identity off
// it, rather than asserting the constants against themselves. Constants
// existing proves nothing about whether a client is ever told them: for this
// binary's whole life the values were knowable and initialize returned two
// fields.
func TestMetadata_ServerReportsItsIdentity(t *testing.T) {
	info := serverInfo(t, "9.9.9")

	require.Equal(t, ServerName, info["name"], "implementation name")
	require.Equal(t, "9.9.9", info["version"], "version must come from the caller, not be hardcoded here")
	require.Equal(t, Title, info["title"], "title is what a client displays")
	require.Equal(t, Description, info["description"], "description is what a client shows beside the name")
	require.Equal(t, WebsiteURL, info["websiteUrl"], "website is what a client links to")

	icons, ok := info["icons"].([]any)
	require.True(t, ok, "a client listing servers renders a blank tile without an icon; got %#v", info["icons"])
	require.NotEmpty(t, icons)
	first, ok := icons[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, IconURL, first["src"])
	require.Equal(t, IconMIMEType, first["mimeType"])
}

// TestMetadata_IsNotEmptyOrPlaceholder is the positive companion.
//
// Every assertion above is an equality check, and equality passes happily when
// both sides are the empty string. Blank the constants and the whole test above
// still passes while the server reports exactly what it reported before this
// change — the same defect, with a green suite over it.
func TestMetadata_IsNotEmptyOrPlaceholder(t *testing.T) {
	require.NotEmpty(t, ServerName)
	require.NotEmpty(t, Title)
	require.NotEmpty(t, Description)
	require.NotEmpty(t, WebsiteURL)
	require.NotEmpty(t, IconURL)

	require.Greater(t, len(Description), 40,
		"a one-word description satisfies the equality check and tells a client nothing")
	require.Contains(t, WebsiteURL, "https://", "a client renders this as a link")
	require.Contains(t, IconURL, "https://", "an http icon is blocked in most clients")
}

// TestMetadata_MatchesPublishedIdentity pins this copy against the values the
// hosted server and the registry manifest carry.
//
// The same four facts now live in three places: server.json in the private
// monorepo (published to registry.modelcontextprotocol.io), the monorepo's own
// constants, and this file. The monorepo asserts its pair against each other.
// This test cannot reach either — it is a separate repository with no
// dependency on them — so it pins the literals instead.
//
// That is weaker than a real cross-check and it is deliberately written out in
// full rather than referring to the constants, so that changing a constant
// fails here and forces whoever changed it to go and change the other two. A
// user reading the registry entry, a user connecting to mcp.lastping.dev, and a
// user running this binary must not be shown three different descriptions.
func TestMetadata_MatchesPublishedIdentity(t *testing.T) {
	require.Equal(t, "lastping-mcp", ServerName)
	require.Equal(t, "LastPing", Title)
	require.Equal(t,
		"Monitoring that agents set up for themselves — cron jobs, CI/CD pipelines and AI agent runs.",
		Description,
		"this sentence is also in server.json and in the hosted server's constants; change all three together")
	require.Equal(t, "https://lastping.dev/agents/", WebsiteURL)
	require.Equal(t, "https://lastping.dev/icon-512.png", IconURL)
}

// TestMetadata_BothBinariesUseOneConstructor guards the reason NewServer
// exists.
//
// The stdio and remote commands previously built their own servers with
// identical calls — identical by coincidence, with nothing preventing drift.
// If someone reintroduces a bare server.NewMCPServer in either command, this
// package's tests keep passing while that binary silently reports no identity
// at all, which is exactly the state being fixed.
//
// A test in this package cannot see package main, so it asserts the property it
// can: that NewServer produces a fully-populated server, and that calling it
// twice with different versions differs only in the version. Anything built
// another way is not covered here, which is why the commands were rewritten to
// have no other way.
func TestMetadata_BothBinariesUseOneConstructor(t *testing.T) {
	a := serverInfo(t, "1.0.0")
	b := serverInfo(t, "2.0.0")

	require.Equal(t, "1.0.0", a["version"])
	require.Equal(t, "2.0.0", b["version"])

	for _, field := range []string{"name", "title", "description", "websiteUrl", "icons"} {
		require.Equal(t, a[field], b[field],
			"%s differs between two builds of the same server", field)
	}
}
