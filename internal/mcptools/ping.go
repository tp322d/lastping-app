package mcptools

// ping.go — the get_ping_instructions tool. It is a thin proxy: the payload
// itself is assembled server-side by GET /api/v1/checks/{id}/ping-instructions,
// never in this process.
//
// WHY: the hosted API builds run_wrapper/hook_install/how_to — the
// hook-install script, the three-mechanism decision rule, and the wrapper
// prompt, which is the most differentiated IP in this product — from a
// private package this open-source binary must never import, directly or
// transitively. So the assembly lives server-side, and every MCP server,
// including this one, is a thin proxy to that endpoint. This tool does not
// build the payload — it decodes the API's response and re-renders it as
// text.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPingTools registers get_ping_instructions — the flagship tool that
// turns a monitor into ready-to-run check-in snippets, so a job or AI agent can
// instrument its own dead-man's-switch in a single conversation.
func registerPingTools(s *server.MCPServer, pingHost string) {
	s.AddTool(
		newTool("get_ping_instructions",
			mcp.WithDescription("Returns what a monitor needs in order to report: its ping URLs, copy-paste snippets (curl_success, curl_start, curl_fail, curl_step, run_example) "+
				"and three reporting mechanisms, for wiring up a new monitor. `reporting_options` holds the rule for choosing between them: "+
				"`how_to` is the manual protocol and works in any agent with no prerequisite (with expect_every_s, a lapse opens an incident); "+
				"`hook_install` is a one-time install that automates the same protocol through hooks and alone sends every state, blocked and note included; "+
				"it is returned only when `tool` is set (claude-code, codex or antigravity), otherwise `hook_install_note` says so; `run_wrapper` puts `lastping run` before a launched command (cron job, CI step, script) "+
				"and reports start, success, fail and cancel. Also returned: `failure_inbox_how_to`, `expectations_how_to` (declare_run_expectations), "+
				"`tracing_how_to` (OpenTelemetry, detailed by get_trace_setup), `otel_env_lines`, export lines whose key placeholder stands for the person's tracing key, and `docs_url`. "+
				"An exporter that cannot set headers can POST to `<ping_url>/v1/traces`, which needs no Authorization header."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
			mcp.WithString("tool",
				mcp.Enum("claude-code", "codex", "antigravity"),
				mcp.Description("Which tool's install `hook_install` carries: claude-code, codex or antigravity. `hook_install` is returned only when this is set. Everything else in the result is the same.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getPingInstructions(ctx, id, req.GetString("tool", ""), pingHost)
		},
	)
}

// PingInstructions mirrors the JSON shape returned by
// GET /api/v1/checks/{id}/ping-instructions, the single source of truth for
// this payload. This tool does not assemble it — it decodes the API response
// into this struct and re-renders it as text, the same pattern Check
// (checks.go) uses for every other resource this package proxies. Field
// names/tags must stay byte-for-byte identical to the API's struct: existing
// MCP clients depend on this exact shape.
type PingInstructions struct {
	MonitorID   string `json:"monitor_id"`
	MonitorName string `json:"monitor_name"`
	PingURL     string `json:"ping_url"`
	SuccessURL  string `json:"success_url"`
	StartURL    string `json:"start_url"`
	FailURL     string `json:"fail_url"`
	StepURL     string `json:"step_url"`
	CurlSuccess string `json:"curl_success"`
	CurlStart   string `json:"curl_start"`
	CurlFail    string `json:"curl_fail"`
	CurlStep    string `json:"curl_step"`
	CronExample string `json:"cron_example"`
	// RunExample is the whole armed run in the order it happens — start, steps,
	// terminal ping — because the individual curl_* lines do not show that all
	// four share one rid, which is the part that is easy to get wrong.
	RunExample string `json:"run_example"`
	// StepTimeoutS is the monitor's stall budget, echoed back so the caller can
	// see whether the steps it is about to emit are actually watched. null means
	// stall detection is off, which is the default.
	StepTimeoutS *int64 `json:"step_timeout_s"`
	// ReportingOptions explains how to choose between the three mechanisms
	// below, keyed on what the monitored thing IS rather than a fixed ranking.
	// It is a field rather than only tool-description prose because the
	// description is read once, when the agent decides to call the tool, while
	// this is in front of it at the moment it decides WHICH block to act on.
	ReportingOptions string `json:"reporting_options"`
	// RunWrapper wraps a command: a separate process does the reporting, so it
	// survives the agent forgetting, being compacted, or being absorbed in a
	// task. Fits when the monitored thing IS a command — cron, CI, a script.
	// Needs a command to wrap, and only ever reports the process's own
	// lifecycle, so it cannot send blocked or note.
	RunWrapper string `json:"run_wrapper"`
	// HookInstall is the one-time install that binds reporting to Claude
	// Code's own event loop -- an OPTIONAL SHORTCUT that automates HowTo's
	// exact same protocol, not a better tier. It is Claude Code specific -- it
	// writes ~/.claude/settings.json and the UserPromptSubmit/Stop/StopFailure
	// hook names -- so it is only the right answer when the monitored thing IS
	// Claude Code specifically: one action that either succeeded or did not,
	// rather than a standing obligation, and the only mechanism that can send
	// every state this product models, including blocked and note. A
	// different AI agent with its own, differently-shaped hook system must
	// not translate this into it; that belongs under HowTo instead.
	HookInstall string `json:"hook_install,omitempty"`
	// HookInstallNote and DocsURL exist only on the MCP result (the REST
	// endpoint does not carry them). hook_install is about 35K characters and
	// is returned only when the caller names a tool, so the note says why the
	// field is absent otherwise. Both are facts about this tool.
	HookInstallNote string `json:"hook_install_note,omitempty"`
	DocsURL         string `json:"docs_url"`
	// HowTo is the manual protocol: the UNIVERSAL path, fitting any agent, any
	// language, any tool, with no prerequisite -- unlike HookInstall (Claude
	// Code only) or RunWrapper (needs a command to wrap). Paired with
	// expect_every_s (the silence floor, set via update_monitor), an agent
	// that quietly stops sending these pings opens a detected `silence`
	// incident instead of leaving its monitor reading healthy forever -- which
	// is why this is offered as the default any agent can rely on, not a
	// fallback ranked behind the other two.
	HowTo      string `json:"how_to"`
	HowToSteps string `json:"how_to_steps"`
	// ExpectationsHowTo mirrors the API struct's ExpectationsHowTo verbatim:
	// how to use declare_run_expectations to commit, before the work starts, to
	// the criteria this run will be judged by. Declared here, byte-for-byte
	// matching, for the same reason every other field on this struct is: an MCP
	// client decodes this exact shape.
	ExpectationsHowTo string `json:"expectations_how_to"`
	// FailureInboxHowTo mirrors the API struct's FailureInboxHowTo verbatim:
	// how to read the agent's open-incident inbox and write a diagnosis back.
	// Declared here, byte-for-byte matching, for the same reason every other
	// field on this struct is: an MCP client decodes this exact shape. Dropping
	// this field silently truncates the JSON this proxy decodes-and-re-renders
	// (see marshalSnippets), which is exactly how it went missing before --
	// this struct fell out of sync with the API's struct after
	// FailureInboxHowTo was added there.
	FailureInboxHowTo string `json:"failure_inbox_how_to"`
	// discovery_how_to is deliberately not decoded: the API still serves it
	// (the console reads it), but this tool omits it from its result. Dropping a
	// field by not naming it here is the same mechanism that once lost
	// failure_inbox_how_to by accident; TestGetPingInstructions_OmitsDiscoveryHowTo
	// pins that this omission is intentional.
	// OtelTracesEndpoint, OtelResourceAttributes, OtelHeadersHint and
	// OtelEnvLines mirror the API struct's fields of the same name verbatim:
	// where to send OpenTelemetry traces for this monitor and the environment
	// lines that do it. Declared here, byte-for-byte matching, for the same
	// reason every other field on this struct is: an MCP client decodes this
	// exact shape.
	OtelTracesEndpoint     string   `json:"otel_traces_endpoint"`
	OtelResourceAttributes string   `json:"otel_resource_attributes"`
	OtelHeadersHint        string   `json:"otel_headers_hint"`
	OtelEnvLines           []string `json:"otel_env_lines"`
	// TracingHowTo mirrors the API struct's field of the same name, for the
	// reason FailureInboxHowTo's comment gives: a proxy that decodes into this
	// struct silently drops what it does not name. The per-tool set-up blocks
	// are get_trace_setup's, not this tool's: carrying all eight here made
	// every get_ping_instructions call pay for set-up it rarely needs.
	TracingHowTo string `json:"tracing_how_to"`
}

// getPingInstructions proxies to GET /api/v1/checks/{id}/ping-instructions
// and re-renders the response as paste-ready text. It builds nothing itself:
// the API is the only place run_wrapper/hook_install/how_to are assembled,
// which is what lets this package stay free of the private prompt-building
// package that produces them.
func (c *APIClient) getPingInstructions(ctx context.Context, id, tool, pingHost string) (*mcp.CallToolResult, error) {
	target := c.BaseURL + "/api/v1/checks/" + id + "/ping-instructions"
	if tool != "" {
		target += "?hook_tool=" + url.QueryEscape(tool)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs, or create_monitor first.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var pi PingInstructions
	if err := json.NewDecoder(resp.Body).Decode(&pi); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	// Defensive only: the API always populates ping_url, so this should never
	// fire. It backfills ping_url alone, not the other URL/curl fields the API
	// derives from it — rebuilding those here would mean re-deriving the
	// server's own assembly logic in this process, which is exactly the
	// duplication this proxy exists to avoid.
	if pi.PingURL == "" {
		pi.PingURL = pingHost + "/" + pi.MonitorID
	}

	// The REST payload always carries hook_install (the console relies on that
	// default). The MCP result carries it only when the caller named a tool.
	if tool == "" {
		pi.HookInstall = ""
		pi.HookInstallNote = "Returned when the tool argument names claude-code, codex or antigravity."
	}
	pi.DocsURL = "https://lastping.dev/mcp/"

	return mcp.NewToolResultText(marshalSnippets(pi)), nil
}

// marshalSnippets renders the instructions with HTML escaping OFF.
//
// encoding/json escapes `&`, `<` and `>` into &, < and > by
// default, a legacy guard for embedding JSON in a <script> tag. Nothing here is
// ever embedded in HTML, and every URL in this payload is a shell command an
// agent is meant to copy verbatim: `.../step?rid=$RID&step=db+migrate` is
// not a URL, and `<run-id>` is not a placeholder anyone recognises.
// The whole point of the tool is that its output is paste-ready.
func marshalSnippets(pi PingInstructions) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pi); err != nil {
		// PingInstructions is strings and an *int64; this cannot fail. Fall
		// back rather than dropping the payload if it somehow does.
		out, _ := json.MarshalIndent(pi, "", "  ")
		return string(out)
	}
	return strings.TrimRight(buf.String(), "\n")
}
