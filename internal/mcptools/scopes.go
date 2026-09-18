package mcptools

// scopes.go — the API key scope every MCP tool needs, in one table.
//
// WHY IT IS HERE WHEN THIS BINARY ENFORCES NOTHING. This package is a thin
// REST client; the hosted LastPing API is the only authorization layer and the
// only thing that can refuse a call. What this table does is put the
// requirement in the tool DESCRIPTION, which is the only thing an agent reads
// before deciding to call a tool.
//
// Without it, an agent holding a "write" key discovers that create_api_key is
// out of reach by calling it and getting a 403 — and a 403 with no anticipated
// cause is indistinguishable from a transient fault, so it retries.
//
// The values MIRROR the hosted API's route table, tool by tool. They are not
// an independent judgement: each tool calls exactly one REST route and takes
// that route's requirement. TestToolScopes_EveryRegisteredToolHasAnEntry fails
// if a tool reaches the server without a row, and the reverse test fails on a
// stale one.

// toolScopes maps every registered tool name to the minimum API key scope it
// needs: "read", "write" or "admin".
var toolScopes = map[string]string{
	// Reads.
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

	// Writes — everything that changes LastPing state, plus test_destination,
	// which changes none of it but sends a real message to a third party.
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

	// Key management. The LIST is admin too: it returns no secret, but key
	// names, prefixes, expiries and lineage are what a caller needs in order to
	// choose which key to revoke.
	"create_api_key": "admin",
	"list_api_keys":  "admin",
	"revoke_api_key": "admin",
}

// scopeSentence is the exact sentence prepended to a tool's description. One
// function rather than 36 hand-written sentences, so the phrasing cannot drift
// between tools and a test can assert on it.
func scopeSentence(scope string) string {
	if scope == "" {
		return ""
	}
	return "Requires an API key with the " + scope + " scope or higher."
}
