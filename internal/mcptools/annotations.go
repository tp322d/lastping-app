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
// This file is the counterpart of the same file in the private monorepo, which
// serves mcp.lastping.dev. The hosted server was corrected first; until this
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

import "github.com/mark3labs/mcp-go/mcp"

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
// a wrong boolean in a long sorted list.
var toolAnnotations = map[string]mcp.ToolAnnotation{
	// ── Read-only ────────────────────────────────────────────────────────
	"export_terraform":      readOnly(),
	"get_agent":             readOnly(),
	"get_alert_templates":   readOnly(),
	"get_monitor":           readOnly(),
	"get_ping_instructions": readOnly(),
	"get_run_history":       readOnly(),
	"list_agents":           readOnly(),
	"list_api_keys":         readOnly(),
	"list_destinations":     readOnly(),
	"list_incidents":        readOnly(),
	"list_monitors":         readOnly(),
	"list_open_incidents":   readOnly(),
	"list_status_pages":     readOnly(),

	// ── Destructive: removes state, and cannot be undone ─────────────────
	// Deleting twice leaves the same absence, so all of these are idempotent.
	"delete_agent":       writes(true, true),
	"delete_destination": writes(true, true),
	"delete_monitor":     writes(true, true),
	"delete_status_page": writes(true, true),
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
	"create_api_key":              writes(false, false),
	"create_destination":          writes(false, false),
	"create_status_page":          writes(false, false),
	"register_agent":              writes(false, false),
	"declare_run_expectations":    writes(false, true),
	"discover_monitors_reconcile": writes(false, true),
	"pause_monitor":               writes(false, true),
	"resume_monitor":              writes(false, true),
	"snooze_monitor":              writes(false, true),
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
	return mcp.NewTool(name, opts...)
}
