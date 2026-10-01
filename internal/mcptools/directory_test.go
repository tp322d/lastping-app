package mcptools

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// listToolsViaServer builds the server both binaries serve (NewServer) and
// asks it for tools/list over JSON-RPC, so the test reads the tool list a
// client is actually handed, titles and annotations as serialised, rather
// than the Go structs they were built from.
func listToolsViaServer(t *testing.T) []mcp.Tool {
	t.Helper()
	s := NewServer("test", "https://ping.test")
	const req = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	raw, err := json.Marshal(s.HandleMessage(context.Background(), []byte(req)))
	require.NoError(t, err)

	var env struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &env), "parse tools/list: %s", raw)
	return env.Result.Tools
}

// The connector directory's review criteria: every tool has a title and
// an explicit readOnlyHint or destructiveHint; names are at most 64
// characters; no description carries prompt-injection phrasing.
func TestDirectory_EveryToolIsListable(t *testing.T) {
	tools := listToolsViaServer(t) // NewServer("test", "https://ping.test") + an in-process tools/list
	require.NotEmpty(t, tools)
	injection := regexp.MustCompile(`(?i)ignore (all |any )?(previous|prior) instructions|you must always|system prompt`)
	for _, tl := range tools {
		require.LessOrEqual(t, len(tl.Name), 64, tl.Name)
		require.NotEmpty(t, tl.Title, "%s has no title", tl.Name)
		require.NotEmpty(t, tl.Annotations.Title, "%s has no annotation title", tl.Name)
		require.Equal(t, tl.Title, tl.Annotations.Title, tl.Name)
		require.LessOrEqual(t, utf8.RuneCountInString(tl.Title), 40, tl.Name)
		require.NotContains(t, tl.Title, "—", tl.Name)
		ro, de := tl.Annotations.ReadOnlyHint, tl.Annotations.DestructiveHint
		require.True(t, ro != nil && de != nil, "%s must state both hints", tl.Name)
		require.False(t, *ro && *de, "%s cannot be read-only and destructive", tl.Name)
		require.False(t, injection.MatchString(tl.Description), tl.Name)
	}
}

// A title row for a tool that is not registered hides a rename, the same
// failure TestAnnotations_TableHasNoRowForAToolThatDoesNotExist guards for
// the hints.
func TestDirectory_TitleTableHasNoStaleRow(t *testing.T) {
	registered := registeredTools(t)
	for name := range toolTitles {
		_, ok := registered[name]
		require.True(t, ok, "toolTitles has a row for %s, which is not registered", name)
	}
}
