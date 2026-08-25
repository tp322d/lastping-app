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
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPingTools registers get_ping_instructions — the flagship tool that
// turns a monitor into ready-to-run check-in snippets, so a job or AI agent can
// instrument its own dead-man's-switch in a single conversation.
func registerPingTools(s *server.MCPServer, pingHost string) {
	s.AddTool(
		mcp.NewTool("get_ping_instructions",
			mcp.WithDescription("Get everything needed to make a monitor actually report: the ping URL, copy-paste check-in snippets, and the three "+
				"MECHANISMS for reporting, returned together. Call this right after create_monitor. "+
				"CHOOSE BY WHAT THE MONITORED THING IS — read `reporting_options` first and pick by that, rather than defaulting to the raw curl list: "+
				"`how_to` — the manual protocol — is the UNIVERSAL path: it works in any agent, any language, any tool, with no prerequisite, so it is the "+
				"default choice for any agent this applies to. Pair it with expect_every_s (the silence floor, set via update_monitor) so an agent that "+
				"quietly stops reporting opens a detected incident instead of leaving its monitor reading healthy. "+
				"If you ARE Claude Code specifically, `hook_install` is available as an OPTIONAL SHORTCUT, not a better tier: a one-time install that binds "+
				"reporting to Claude Code's own hooks (UserPromptSubmit, Stop, StopFailure), automating how_to's exact same protocol so reporting becomes a "+
				"property of your event loop instead of something you must remember — and it is the only mechanism that can send every state this product "+
				"models, including blocked and note. hook_install is Claude Code specific: if you are a DIFFERENT AI agent — even one with its own hook or "+
				"event system, Cursor, Windsurf, Codex, a custom framework — do NOT translate its steps into your own hooks; the event semantics differ and a "+
				"translated install can pass its own verification while never reporting, so use `how_to` instead. "+
				"If what you are monitoring is launched as a command instead — a cron job, a CI step, a script, or an agent started from a shell — use "+
				"`run_wrapper`: wrap the command with `lastping run` and a separate process reports for you, so nothing has to be remembered; the tradeoff is "+
				"that it reports the process's own lifecycle (start, success, fail, cancel) and has no way to send blocked or note. "+
				"Whichever you choose, the underlying protocol is the same: the success ping at the END of the work, the fail URL if it failed, "+
				"the start ping first for long or possibly-hung runs (this enables overrun / never-finished detection), "+
				"and a step (curl_step) as each stage completes so a run that wedges mid-way is caught by name rather than only when its whole budget expires. "+
				"Also read `expectations_how_to`: before you start work, use declare_run_expectations to say how THIS run should be judged when it closes — "+
				"a one-time, unchangeable commitment that replaces the run grading itself. "+
				"And `discovery_how_to`, which is about the OTHER jobs on this host or in this repo: how to find the scheduled work nobody is watching yet "+
				"and propose it, rather than monitoring only the one thing you were asked about."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors)."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getPingInstructions(ctx, id, pingHost)
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
	HookInstall string `json:"hook_install"`
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
	// DiscoveryHowTo mirrors the API struct's DiscoveryHowTo verbatim: how to
	// find the scheduled work on this host or in this repo that nobody is
	// watching yet, and propose it -- a project-scoped instruction served from
	// a check-scoped payload, because this is the payload an agent already
	// reads.
	//
	// There is no compiler check that this mirror still matches the API's
	// struct and there cannot be one, since the two live in repositories that
	// must not share code; the guard is
	// TestGetPingInstructions_IncludesEveryHowToField, which decodes the
	// rendered output into a generic map so a dropped field fails on a missing
	// key rather than passing on both sides equally.
	DiscoveryHowTo string `json:"discovery_how_to"`
}

// getPingInstructions proxies to GET /api/v1/checks/{id}/ping-instructions
// and re-renders the response as paste-ready text. It builds nothing itself:
// the API is the only place run_wrapper/hook_install/how_to are assembled,
// which is what lets this package stay free of the private prompt-building
// package that produces them.
func (c *APIClient) getPingInstructions(ctx context.Context, id, pingHost string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/ping-instructions", nil)
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
