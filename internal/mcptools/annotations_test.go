package mcptools

import (
	"sort"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// registeredTools returns the tools Register actually adds to a server, keyed
// by name, so these tests read what the server WILL SERVE rather than what
// toolAnnotations says it should. Asserting the table against itself would
// pass with every registration still calling mcp.NewTool and no annotation
// reaching the wire at all — which is precisely the shipped bug.
func registeredTools(t *testing.T) map[string]mcp.Tool {
	t.Helper()

	s := NewServer("test", "https://ping.example.test")

	out := make(map[string]mcp.Tool)
	for name, st := range s.ListTools() {
		out[name] = st.Tool
	}
	require.NotEmpty(t, out, "Register added no tools")
	return out
}

// TestAnnotations_EveryRegisteredToolHasAnEntry is the guard that stops this
// defect recurring.
//
// The failure it prevents is not "someone edits the table wrongly" but
// "someone adds a tool and never thinks about the table at all". A tool with
// no row gets no annotation from newTool, mcp-go emits the MCP defaults, and
// the new tool silently advertises itself as destructive, non-idempotent and
// open-world — exactly the state all 36 were in before this file existed.
//
// It reads the names off a real server rather than from the golden file, so a
// tool registered but missing from the golden is still caught here.
func TestAnnotations_EveryRegisteredToolHasAnEntry(t *testing.T) {
	var missing []string
	for name := range registeredTools(t) {
		if _, ok := toolAnnotations[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"these tools have no row in toolAnnotations, so they inherit the MCP "+
			"defaults (destructiveHint true, openWorldHint true). Add a row in "+
			"annotations.go: %v", missing)
}

// TestAnnotations_TableHasNoRowForAToolThatDoesNotExist is the other direction.
//
// A stale row is harmless at runtime but is a lie in the file people read to
// learn what this server does, and it hides a rename: rename a tool without
// updating the table and the new name silently falls back to the defaults
// while the old row still sits there looking correct.
func TestAnnotations_TableHasNoRowForAToolThatDoesNotExist(t *testing.T) {
	registered := registeredTools(t)

	var stale []string
	for name := range toolAnnotations {
		if _, ok := registered[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	require.Empty(t, stale, "toolAnnotations has rows for tools that are not registered: %v", stale)
}

// TestAnnotations_ReachTheServedTool asserts the hints are on the tool the
// server hands out, not merely in the map.
//
// newTool prepends the annotation to the option list. If that wiring broke —
// a registration reverted to mcp.NewTool, the prepend dropped, the option
// overwritten by a later one — the table would still be perfectly correct and
// every value still wrong on the wire. This test is what makes the table load
// bearing.
func TestAnnotations_ReachTheServedTool(t *testing.T) {
	tools := registeredTools(t)

	for name, want := range toolAnnotations {
		tool, ok := tools[name]
		if !ok {
			continue // covered by the stale-row test above
		}
		got := tool.Annotations

		require.NotNil(t, got.ReadOnlyHint, "%s: readOnlyHint absent from the served tool", name)
		require.NotNil(t, got.DestructiveHint, "%s: destructiveHint absent from the served tool", name)
		require.NotNil(t, got.IdempotentHint, "%s: idempotentHint absent from the served tool", name)
		require.NotNil(t, got.OpenWorldHint, "%s: openWorldHint absent from the served tool", name)

		require.Equal(t, *want.ReadOnlyHint, *got.ReadOnlyHint, "%s: readOnlyHint", name)
		require.Equal(t, *want.DestructiveHint, *got.DestructiveHint, "%s: destructiveHint", name)
		require.Equal(t, *want.IdempotentHint, *got.IdempotentHint, "%s: idempotentHint", name)
		require.Equal(t, *want.OpenWorldHint, *got.OpenWorldHint, "%s: openWorldHint", name)
	}
}

// TestAnnotations_NoReaderClaimsToWrite pins the specific claim that was wrong
// in production, by name rather than by counting.
//
// Naming the tools makes the test fail loudly if one of them ever stops being
// a pure read — which is the moment a human should look, not a moment to
// quietly update a number. The list is every tool a caller would reasonably
// expect to be safe to run without being asked first.
func TestAnnotations_NoReaderClaimsToWrite(t *testing.T) {
	readers := []string{
		"export_terraform",
		"get_agent",
		"get_alert_templates",
		"get_incident",
		"get_monitor",
		"get_ping_instructions",
		"get_run_history",
		"list_agents",
		"list_api_keys",
		"list_destinations",
		"list_incidents",
		"list_monitors",
		"list_open_incidents",
		"list_status_pages",
	}

	tools := registeredTools(t)
	for _, name := range readers {
		tool, ok := tools[name]
		require.True(t, ok, "%s is not registered; if it was renamed, update this list deliberately", name)

		require.True(t, *tool.Annotations.ReadOnlyHint, "%s must advertise readOnlyHint true", name)
		require.False(t, *tool.Annotations.DestructiveHint,
			"%s advertises destructiveHint true — a host that honours annotations will "+
				"prompt before every read", name)
	}
}

// TestAnnotations_EveryDestructiveToolSaysSo is the positive companion.
//
// Without it, TestAnnotations_NoReaderClaimsToWrite degrades into "nothing is
// destructive", which would pass with every hint set to false and would be a
// worse lie than the one being fixed: a delete that does not ask.
//
// The two lists together also cover the whole surface, so a tool cannot be
// quietly moved from one to the other without a test naming it.
func TestAnnotations_EveryDestructiveToolSaysSo(t *testing.T) {
	destructive := []string{
		// removes state outright
		"delete_agent",
		"delete_destination",
		"delete_monitor",
		"delete_status_page",
		"revoke_api_key",
		// overwrites state that already existed
		"create_monitor",     // upserts by slug: rewrites an existing monitor
		"set_alert_template", // an empty template clears the existing one
		"set_route",          // replaces the whole destination set for an event
		"update_agent",
		"update_destination",
		"update_monitor",
		"update_status_page",
	}

	tools := registeredTools(t)
	for _, name := range destructive {
		tool, ok := tools[name]
		require.True(t, ok, "%s is not registered; if it was renamed, update this list deliberately", name)

		require.False(t, *tool.Annotations.ReadOnlyHint, "%s must not advertise readOnlyHint", name)
		require.True(t, *tool.Annotations.DestructiveHint,
			"%s can overwrite or remove state that existed before the call, so a host "+
				"must be told to confirm it", name)
	}
}

// TestAnnotations_OnlyTestDestinationIsOpenWorld pins the one row that differs.
//
// openWorldHint true means "the result depends on something we do not
// control". That is true of exactly one tool here, and it matters that it
// stays exactly one: every other tool talks only to the LastPing API, and
// marking them open-world would tell a client their results are less
// reproducible than they are.
func TestAnnotations_OnlyTestDestinationIsOpenWorld(t *testing.T) {
	var openWorld []string
	for name, tool := range registeredTools(t) {
		if tool.Annotations.OpenWorldHint != nil && *tool.Annotations.OpenWorldHint {
			openWorld = append(openWorld, name)
		}
	}
	sort.Strings(openWorld)
	require.Equal(t, []string{"test_destination"}, openWorld,
		"test_destination reaches a third-party endpoint; nothing else here leaves the LastPing API")
}
