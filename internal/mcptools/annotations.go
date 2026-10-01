package mcptools

// annotations.go — the read/write character of every MCP tool, in one table.
//
// WHAT WAS WRONG
//
// Nothing in this package set tool annotations, so mcp-go emitted the MCP
// spec's defaults for all 36 tools: readOnlyHint false, destructiveHint TRUE,
// idempotentHint false, openWorldHint true. The spec defaults destructiveHint
// to true precisely because a server that says nothing must be assumed
// dangerous — which is the right default and the wrong description of this
// server. get_monitor and list_monitors advertised themselves as destructive,
// non-idempotent and open-world, identically to delete_monitor.
//
// That is not cosmetic. A host that honours annotations — Claude Desktop among
// them — gates a destructive tool behind a confirmation and may auto-approve a
// read-only one. Declaring every read destructive opts every read out of the
// auto-approve path, so an agent has to be confirmed through list_monitors,
// while simultaneously making delete_monitor look no more alarming than
// reading. The signal was uniformly wrong in both directions at once.
//
// Smithery's quality score does not catch this: it scores annotations as
// present (36/36) because presence is all it inspects. Being scored full marks
// for a field whose every value was wrong is the reason this table exists
// rather than a tweak to a few tools.
//
// This table matches the hosted server at mcp.lastping.dev. The hosted server
// was corrected first; until this
// landed, the server people INSTALL described its tools differently from the
// server people CONNECT to — the two disagreeing not about which tools exist,
// which wantTools already guards, but about what they do.
//
// HOW IT IS APPLIED
//
// Through newTool below, which every registration calls instead of
// mcp.NewTool. Routing all 36 through one helper is deliberate: a table plus
// 36 remembering-to-call-it sites would drift on the first tool anyone added
// in a hurry. TestAnnotations_EveryRegisteredToolHasAnEntry fails if a tool
// reaches the server without a row here, so a new tool cannot quietly inherit
// the spec default again.
//
// HOW THE FOUR HINTS ARE READ HERE
//
//   - ReadOnlyHint      does not modify anything. Every list_*, get_* and the
//     Terraform export.
//   - DestructiveHint   may overwrite or remove state that existed before.
//     True for the deletes and the revoke, and also for
//     the in-place writers: an update_* merge-patch
//     overwrites the fields it names, set_route replaces
//     a whole destination set, and create_monitor
//     upserts by slug, so it can silently rewrite the
//     configuration of a monitor that already exists.
//     False for genuinely additive writes.
//   - IdempotentHint    repeating the call leaves the same state. True for
//     deletes, in-place updates and toggles; false where
//     each call produces another object or another
//     side effect (a new API key, another incident note,
//     another test delivery).
//   - OpenWorldHint     interacts with entities outside the LastPing API.
//     False almost everywhere: this is a closed domain,
//     the project the key scopes to. True only for
//     test_destination, whose whole purpose is to reach
//     a third-party endpoint whose behaviour we do not
//     control.
//
// Where a judgement was close, it went to the more cautious value, because the
// cost of a needless confirmation prompt is a click and the cost of a missing
// one is an overwritten monitor.

import (
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func boolPtr(b bool) *bool { return &b }

// readOnly describes a tool that only reads. Reads are idempotent by
// construction, and destructiveHint carries no meaning once readOnlyHint is
// true — it is still written false rather than left to the spec default, so a
// client reading the raw JSON is not told "destructive" about a getter.
func readOnly() mcp.ToolAnnotation {
	return mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}
}

// writes describes a tool that changes state. destructive says whether it can
// overwrite or remove something that was already there; idempotent says
// whether calling it twice differs from calling it once.
func writes(destructive, idempotent bool) mcp.ToolAnnotation {
	return mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(destructive),
		IdempotentHint:  boolPtr(idempotent),
		OpenWorldHint:   boolPtr(false),
	}
}

// toolAnnotations is the whole classification, and the only place it lives.
//
// Grouped by character rather than alphabetically so that a wrong row is
// visible as a row in the wrong group, which is easier to spot in review than
// a wrong boolean in a long sorted list. Alphabetical within each group.
var toolAnnotations = map[string]mcp.ToolAnnotation{
	// ── Read-only ────────────────────────────────────────────────────────
	"export_terraform":       readOnly(),
	"get_agent":              readOnly(),
	"get_agent_dependencies": readOnly(),
	"get_agent_usage":        readOnly(),
	"get_alert_templates":    readOnly(),
	"get_incident":           readOnly(),
	"get_monitor":            readOnly(),
	"get_ping_instructions":  readOnly(),
	"get_run":                readOnly(),
	"get_run_history":        readOnly(),
	"get_trace_diagnostics":  readOnly(),
	"get_trace_setup":        readOnly(),
	"list_agents":            readOnly(),
	"list_api_keys":          readOnly(),
	"list_deliveries":        readOnly(),
	"list_dependencies":      readOnly(),
	"list_destinations":      readOnly(),
	"list_discovered_agents": readOnly(),
	"list_incidents":         readOnly(),
	"list_monitors":          readOnly(),
	"list_open_incidents":    readOnly(),
	"list_runs":              readOnly(),
	"list_status_pages":      readOnly(),

	// ── Destructive: removes state, and cannot be undone ─────────────────
	// Deleting twice leaves the same absence, so all of these are idempotent.
	"delete_agent":       writes(true, true),
	"delete_destination": writes(true, true),
	"delete_monitor":     writes(true, true),
	"delete_route":       writes(true, true),
	"delete_status_page": writes(true, true),
	"regenerate_api_key": writes(true, false),
	"revoke_api_key":     writes(true, true),

	// ── Destructive: overwrites state that already existed ───────────────
	// create_monitor is here, not with the additive creates, because it
	// upserts by slug: called with the slug of a monitor that exists, it
	// rewrites that monitor's configuration. The tool's own description says
	// so ("or update an existing one if slug matches"). set_route is the
	// starkest of them — it replaces the entire destination set for an event
	// type, so every destination omitted from the call is unrouted.
	"create_monitor":     writes(true, true),
	"set_alert_template": writes(true, true),
	"set_route":          writes(true, true),
	"update_agent":       writes(true, true),
	"update_destination": writes(true, true),
	"update_monitor":     writes(true, true),
	"update_status_page": writes(true, true),

	// ── Additive: creates or toggles, destroys nothing ───────────────────
	// Idempotent where a repeat call converges on the same state (pausing an
	// already-paused monitor, re-declaring the same run expectations,
	// re-running discovery against an unchanged repo). Not idempotent where
	// each call produces another object or another delivery.
	"add_incident_note":           writes(false, false),
	"adopt_discovered_agent":      writes(false, true),
	"create_api_key":              writes(false, false),
	"create_destination":          writes(false, false),
	"create_ingest_key":           writes(false, false),
	"create_status_page":          writes(false, false),
	"declare_run_expectations":    writes(false, true),
	"discover_monitors_reconcile": writes(false, true),
	"pause_monitor":               writes(false, true),
	"register_agent":              writes(false, false),
	"resume_monitor":              writes(false, true),
	"snooze_monitor":              writes(false, true),
}

// toolTitles is each tool's display name: what Claude and other hosts show a
// person in the tool list and in a confirmation prompt, where the snake_case
// name reads as an identifier. Sentence case, at most 40 characters, no em
// dashes, and never a promise the tool does not keep: create_monitor says it
// may update, because it upserts by slug. TestDirectory_EveryToolIsListable
// fails on a registered tool without a row here.
var toolTitles = map[string]string{
	"add_incident_note":           "Add incident note",
	"adopt_discovered_agent":      "Adopt discovered trace source",
	"create_api_key":              "Create API key",
	"create_destination":          "Create alert destination",
	"create_ingest_key":           "Create tracing key",
	"create_monitor":              "Create or update monitor",
	"create_status_page":          "Create status page",
	"declare_run_expectations":    "Declare run expectations",
	"delete_agent":                "Delete agent",
	"delete_destination":          "Delete alert destination",
	"delete_monitor":              "Delete monitor",
	"delete_route":                "Delete alert route",
	"delete_status_page":          "Delete status page",
	"discover_monitors_reconcile": "Reconcile discovered jobs as monitors",
	"export_terraform":            "Export Terraform configuration",
	"get_agent":                   "Get agent details",
	"get_agent_dependencies":      "Get one agent's dependencies",
	"get_agent_usage":             "Get agent model usage",
	"get_alert_templates":         "Get alert templates",
	"get_incident":                "Get incident timeline",
	"get_monitor":                 "Get monitor details",
	"get_ping_instructions":       "Get ping instructions",
	"get_run":                     "Get run details",
	"get_run_history":             "Get run history",
	"get_trace_diagnostics":       "Get trace diagnostics",
	"get_trace_setup":             "Get tracing setup steps",
	"list_agents":                 "List agents",
	"list_api_keys":               "List API keys",
	"list_deliveries":             "List alert deliveries",
	"list_dependencies":           "List project dependencies",
	"list_destinations":           "List alert destinations",
	"list_discovered_agents":      "List discovered trace sources",
	"list_incidents":              "List monitor incidents",
	"list_monitors":               "List monitors",
	"list_open_incidents":         "List open incidents",
	"list_runs":                   "List runs",
	"list_status_pages":           "List status pages",
	"pause_monitor":               "Pause monitor",
	"regenerate_api_key":          "Regenerate API key",
	"register_agent":              "Register agent",
	"resume_monitor":              "Resume monitor",
	"revoke_api_key":              "Revoke API key",
	"set_alert_template":          "Set alert template",
	"set_route":                   "Set alert route",
	"snooze_monitor":              "Set or clear maintenance window",
	"test_destination":            "Test alert destination",
	"update_agent":                "Update agent",
	"update_destination":          "Update alert destination",
	"update_monitor":              "Update monitor",
	"update_status_page":          "Update status page",
}

// testDestinationAnnotation is the one tool that reaches outside LastPing.
//
// Every other tool talks only to the LastPing API — a closed domain scoped to
// the caller's project, where a result depends on nothing we do not store.
// test_destination exists to deliver a message through a real Slack, Discord,
// Telegram or webhook endpoint, so its outcome depends on a third party being
// reachable and behaving. That is exactly what openWorldHint is for, and it is
// the only row where it is true.
var testDestinationAnnotation = mcp.ToolAnnotation{
	ReadOnlyHint:    boolPtr(false),
	DestructiveHint: boolPtr(false),
	IdempotentHint:  boolPtr(false),
	OpenWorldHint:   boolPtr(true),
}

func init() { toolAnnotations["test_destination"] = testDestinationAnnotation }

// newTool builds a tool with its annotations already attached.
//
// Every registration in this package calls this instead of mcp.NewTool. The
// annotation is applied FIRST, so an individual tool can still override a hint
// by passing mcp.WithToolAnnotation after it — no tool does today, but the
// ordering means a future one-off does not have to fight the table.
//
// A name with no row gets no annotation rather than a guessed one: guessing
// would re-create the exact defect this file fixes, quietly. The gap is caught
// by TestAnnotations_EveryRegisteredToolHasAnEntry instead, at build time,
// where it is a failing test rather than a wrong hint in production.
func newTool(name string, opts ...mcp.ToolOption) mcp.Tool {
	if ann, ok := toolAnnotations[name]; ok {
		opts = append([]mcp.ToolOption{mcp.WithToolAnnotation(ann)}, opts...)
	}
	t := mcp.NewTool(name, opts...)
	// The title is set on both fields after construction, because
	// mcp.WithToolAnnotation replaces the whole annotation struct: set as an
	// option, Annotations.Title would be wiped by the table's row. The spec
	// says a client prefers Tool.Title and falls back to Annotations.Title,
	// and older clients read only the latter, so both carry it.
	if title := toolTitles[name]; title != "" {
		t.Title = title
		t.Annotations.Title = title
	}
	// The required scope (scopes.go) is PREPENDED to the description rather
	// than passed as another option, because mcp.WithDescription assigns
	// rather than appends and the per-tool text is written at the call site.
	// It goes first, not last: some descriptions run past 3.5k characters
	// (discover_monitors_reconcile), and a sentence appended after all of that
	// is one a context-constrained agent, or a client UI that truncates long
	// descriptions, is least likely to ever reach. The whole point is to let
	// an agent anticipate a 403 before calling — that only works if the
	// requirement is the first thing read, not the last. Same
	// table-plus-one-helper shape as the annotations above, and for the same
	// reason: 36 sites remembering to add a sentence would drift on the first
	// tool anyone added in a hurry.
	if s := scopeSentence(toolScopes[name]); s != "" {
		t.Description = s + " " + strings.TrimSpace(t.Description)
	}
	return t
}
