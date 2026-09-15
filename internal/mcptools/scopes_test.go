package mcptools

import (
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// wantToolScopes is the scope table copied from the hosted server, tool by
// tool, and it is deliberately a SECOND copy rather than a reference to
// toolScopes: a test that reads the table it is checking asserts only that the
// table equals itself, and would pass with every value wrong. This list is the
// thing a human updates, by reading the hosted server's route table, when a
// tool's requirement changes.
var wantToolScopes = map[string]string{
	// Reads (13).
	"export_terraform":      "read",
	"get_agent":             "read",
	"get_alert_templates":   "read",
	"get_incident":          "read",
	"get_monitor":           "read",
	"get_ping_instructions": "read",
	"get_run":               "read",
	"get_run_history":       "read",
	"list_agents":           "read",
	"list_destinations":     "read",
	"list_incidents":        "read",
	"list_monitors":         "read",
	"list_open_incidents":   "read",
	"list_status_pages":     "read",

	// Writes (21) — test_destination is here because it sends a real message
	// to a third party, even though it changes no LastPing state.
	"add_incident_note":           "write",
	"create_destination":          "write",
	"create_monitor":              "write",
	"create_status_page":          "write",
	"declare_run_expectations":    "write",
	"delete_agent":                "write",
	"delete_destination":          "write",
	"delete_monitor":              "write",
	"delete_status_page":          "write",
	"discover_monitors_reconcile": "write",
	"pause_monitor":               "write",
	"register_agent":              "write",
	"resume_monitor":              "write",
	"set_alert_template":          "write",
	"set_route":                   "write",
	"snooze_monitor":              "write",
	"test_destination":            "write",
	"update_agent":                "write",
	"update_destination":          "write",
	"update_monitor":              "write",
	"update_status_page":          "write",

	// Key management (3). The LIST is admin too: it returns no secret, but key
	// names, prefixes, expiries and lineage are what a caller needs in order
	// to choose which key to revoke.
	"create_api_key": "admin",
	"list_api_keys":  "admin",
	"revoke_api_key": "admin",
}

// registeredNames returns every tool Register adds, read off a real server.
func registeredNames(t *testing.T) []string {
	t.Helper()
	s := server.NewMCPServer("lastping-test", "test")
	Register(s, "https://ping.example.test")
	var names []string
	for name := range s.ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "Register added no tools")
	return names
}

// TestToolScopes_EveryRegisteredToolHasAnEntry is the same guard shape as
// TestAnnotations_EveryRegisteredToolHasAnEntry, and it exists for the same
// reason: the failure to catch is not "someone chose the wrong scope" but
// "someone adds a tool and never thinks about scopes at all". A tool with no
// row here tells the agent nothing about what credential it needs, and the
// agent finds out from a 403 it could not have anticipated.
func TestToolScopes_EveryRegisteredToolHasAnEntry(t *testing.T) {
	var missing []string
	for _, name := range registeredNames(t) {
		if _, ok := toolScopes[name]; !ok {
			missing = append(missing, name)
		}
	}
	require.Empty(t, missing,
		"these tools have no row in toolScopes, so their description does not say "+
			"which API key scope they need. Add a row in scopes.go: %v", missing)
}

// TestToolScopes_TableHasNoRowForAToolThatDoesNotExist is the other direction —
// a stale row is a lie in the file people read, and it hides a rename.
func TestToolScopes_TableHasNoRowForAToolThatDoesNotExist(t *testing.T) {
	live := map[string]bool{}
	for _, name := range registeredNames(t) {
		live[name] = true
	}
	var stale []string
	for name := range toolScopes {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	require.Empty(t, stale, "toolScopes names tools that are not registered: %v", stale)
}

// TestToolScopes_MatchTheHostedServer compares the shipped table against the
// independently-written copy above, so a value that drifts from the hosted
// server's route table fails here rather than being discovered by an agent
// holding the wrong credential.
func TestToolScopes_MatchTheHostedServer(t *testing.T) {
	require.Len(t, wantToolScopes, 38,
		"the hosted server exposes 38 tools; update wantToolScopes deliberately when that changes")
	require.Equal(t, wantToolScopes, toolScopes,
		"toolScopes has drifted from the hosted server's per-tool requirement")
}

// TestToolScopes_ReachTheAgentInTheToolDescription is the one that stops this
// table becoming a dead map. The description is the ONLY thing an agent reads
// before deciding to call a tool; a scope recorded in Go and never emitted
// tells nobody anything. Read off a real server, not off the table, and
// table-driven over all 36 so a single tool that loses its sentence is named
// by the failure.
//
// Asserted by POSITION, not Contains: some descriptions run past 3.5k
// characters (discover_monitors_reconcile), and a requirement buried at the
// end is one a context-constrained agent, or a client UI that truncates long
// descriptions, may never reach. The sentence has to be the FIRST thing in the
// description for "anticipate the 403 before calling" to hold in practice, not
// just in principle — a bare Contains would pass whether the sentence were
// first or last.
func TestToolScopes_ReachTheAgentInTheToolDescription(t *testing.T) {
	s := server.NewMCPServer("lastping-test", "test")
	Register(s, "https://ping.example.test")
	served := s.ListTools()

	for name, want := range wantToolScopes {
		t.Run(name, func(t *testing.T) {
			st, ok := served[name]
			require.True(t, ok, "%s is not registered", name)

			sentence := "Requires an API key with the " + want + " scope or higher."
			require.True(t, strings.HasPrefix(st.Tool.Description, sentence),
				"tool %q must lead its description with %q, got: %.120q",
				name, sentence, st.Tool.Description)

			require.NotEqual(t, sentence, strings.TrimSpace(st.Tool.Description),
				"%s must still describe what it does after the scope sentence", name)
		})
	}
}

// TestScopeSentence_IsEmptyForAnUnknownTool pins the one branch the prepend
// depends on: a tool with no row must get no sentence at all rather than
// "Requires an API key with the  scope or higher.", which would be a grammar
// error shipped to every agent and a claim about a requirement nobody set.
func TestScopeSentence_IsEmptyForAnUnknownTool(t *testing.T) {
	require.Empty(t, scopeSentence(""))
	require.Equal(t, "Requires an API key with the admin scope or higher.", scopeSentence("admin"))
}
