package mcptools

// discovery.go — discover_monitors_reconcile, a pure proxy to
// POST /api/v1/discovery/reconcile.
//
// Discovery is the half of the product that removes the form: an agent scans a
// repository or a host, works out what ought to be monitored, and hands
// LastPing the list. All the scanning is agent-side by design — LastPing never
// reads a customer repository — so the service supplies exactly three things:
// what to look for, a way to diff a scan against what already exists, and a
// bulk create. The endpoint shipped and deployed before this tool existed, and
// nothing called it, which is the tell that the surface an agent actually has
// was the missing piece rather than the capability.
//
// THERE IS DELIBERATELY NO SECOND TOOL HERE. An earlier plan called for a
// list_monitored_targets alongside this one; it would answer a question
// list_monitors already answers, because the monitor payload carries
// source_kind and source_ref on every monitor it returns. A second tool for the
// same fact is surface an agent has to choose between, for nothing.
//
// THIS FILE DOES NO VALIDATION OF ITS OWN, exactly as run_expectations.go does
// none: the API is the single source of truth for what a valid source is (the
// same validation POST /api/v1/checks uses), for when tz is mandatory, and
// for the caps on payload size and monitor count. Duplicating any of those
// here would produce a second answer that drifts. The `sources` argument is
// decoded only far enough to shape the outgoing request body.
//
// It also builds no prompt. The instruction set that teaches an agent WHERE to
// scan and HOW to read a host's timezone is assembled server-side, from a
// private package this open-source binary must never import — the same
// boundary that keeps the ping-instruction prompts out of this process (see
// ping.go). What that costs is a description written out longhand here rather
// than composed from the shared constants; what it buys is a public MCP binary
// that ships no private prompt-building code. The description below is
// therefore the ONE place an agent learns the three rules before it calls, and
// it is byte-identical to the hosted server's.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// maxReconcileResponseBytes bounds the diff this tool will read back. A
// project holds at most 100 monitors and the response can name each of them at
// most once across the three arrays, so this is roughly two orders of
// magnitude of headroom over the largest legitimate answer — it exists so a
// misconfigured BaseURL pointing at something that is not the API cannot
// stream an unbounded body into the agent's context.
const maxReconcileResponseBytes = 8 << 20

// discoverReconcileDesc carries three facts that exist nowhere else on this
// surface: what reconcile creates, that it never deletes, pauses or edits, and
// which zone each source kind's schedule fires in. They are stated as facts
// about the tool (directory policy); the propose-before-creating procedure
// lives in the public lastping-setup skill.
//
// The API can require that a tz be stated for a host-local scanner, but it
// cannot tell an informed "UTC" from a reflexive one, which is why the zone
// facts stay in the description.
const discoverReconcileDesc = "Turns a scan of a repository or host (`sources`, every scheduled job found) into monitors. " +
	"Creates one monitor per source not already monitored; never deletes, pauses or edits existing monitors, so a scheduled re-run is safe drift detection. " +
	"Returns `created`, `existing` (unmodified, hand-tuned values intact) and `orphaned` (source not in this scan; still running and alerting). " +
	"Each created monitor can page a person; this endpoint has no delete path. " +
	"source_kind plus source_ref is the diff key, so source_ref has to stay stable: a changed ref creates a duplicate; " +
	"a crontab ref names the file and the command, so each line is its own source. " +
	"Kinds: 'crontab' (crontab -l, /etc/cron.d/*, /etc/crontab), 'github-actions' (on.schedule.cron), " +
	"'k8s-cronjob' (spec.schedule), 'systemd-timer' (OnCalendar=). A source without schedule_cron becomes on-demand. " +
	"crontab and systemd-timer fire in the host's local time (`timedatectl show -p Timezone --value` or `readlink /etc/localtime` prints it); " +
	"github-actions and k8s-cronjob in UTC. A stated tz is stored as given: the API cannot tell a guessed 'UTC' from one read from the host. " +
	"list_monitors shows each source."

// discoverReconcileSourcesDesc describes the one argument the tool takes. The
// rejections named here are 400s that are otherwise unanticipated.
const discoverReconcileSourcesDesc = "The full scan as a JSON array string, e.g. " +
	`'[{"source_kind":"crontab","source_ref":"/etc/cron.d/backup:/usr/local/bin/backup.sh","schedule_cron":"0 3 * * *","tz":"Europe/Berlin"}]'` + ". " +
	"Fields: source_kind, source_ref (required), name (default source_ref), schedule_cron (5-field, when one was read), " +
	"tz (IANA zone the cron fires in; required on a crontab or systemd-timer entry with schedule_cron), " +
	"suggested_expect_every_s (overrides the silence floor). " +
	"An omitted source is reported orphaned; '[]' orphans every discovered monitor, deleting none. " +
	"At most 1000 entries; a repeated source_kind/source_ref pair is rejected. The 100-monitor cap covers the whole batch; " +
	"one invalid entry rejects it all."

// reconcileProblemText renders an RFC 7807 rejection from
// /api/v1/discovery/reconcile, INCLUDING its `fix` field.
//
// c.problem reads title, status and detail only, which is right for endpoints
// whose detail is self-explanatory. It is not right here. Every rejection this
// endpoint issues carries a machine-matchable code and a one-sentence remedy,
// and for the rejection that matters most the remedy is the whole message:
// TIMEZONE_REQUIRED's fix names the command that reads the host's zone. An
// agent handed "tz is required for a crontab source" and nothing else has been
// given the rule without the means to satisfy it, and its most likely next move
// is to send "UTC" — the single failure this endpoint cannot detect.
//
// Falls back to the status line when the body is not a problem document, so a
// proxy's HTML error page cannot be mistaken for an empty rejection.
func reconcileProblemText(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReconcileResponseBytes))
	if err != nil {
		return fmt.Sprintf("HTTP %d (response body could not be read: %v)", resp.StatusCode, err)
	}
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
		Code   string `json:"code"`
		Fix    string `json:"fix"`
	}
	if uErr := json.Unmarshal(raw, &p); uErr != nil || p.Title == "" {
		return undecodableProblem(resp.StatusCode).Error()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (HTTP %d)", p.Title, resp.StatusCode)
	if p.Code != "" {
		fmt.Fprintf(&b, " [%s]", p.Code)
	}
	if p.Detail != "" {
		fmt.Fprintf(&b, ": %s", p.Detail)
	}
	if p.Fix != "" {
		fmt.Fprintf(&b, "\nHow to fix it: %s", p.Fix)
	}
	return b.String()
}

// registerDiscoveryTools registers discover_monitors_reconcile.
func registerDiscoveryTools(s *server.MCPServer) {
	s.AddTool(
		newTool("discover_monitors_reconcile",
			mcp.WithDescription(discoverReconcileDesc),
			mcp.WithString("sources", mcp.Required(), mcp.Description(discoverReconcileSourcesDesc)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := req.RequireString("sources")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.discoverMonitorsReconcile(ctx, raw)
		},
	)
}

// discoverMonitorsReconcile proxies POST /api/v1/discovery/reconcile.
//
// rawSources is decoded only to shape the outgoing body — every semantic rule
// (which kinds exist, when tz is mandatory, the ref's character set, the
// payload and monitor caps) is the API's, and is not repeated here.
//
// It decodes into a POINTER for the same reason the API's own request struct
// holds one: an EMPTY array and NO array are different
// claims, and JSON null decodes into a slice by leaving it untouched, so a
// plain []discoverySourceArg cannot tell them apart. `[]` is a scanner
// reporting that it found nothing, which legitimately makes every discovered
// monitor an orphan and is exactly the report a user whose repository just
// lost its last scheduled job needs to see. A literal `null` is a caller that
// has produced nothing at all, and forwarding it as an empty scan would hand
// that caller a drift report claiming the whole fleet is dead. The API answers
// 400 MISSING_SOURCES for the second case; this rejects it one hop earlier,
// where the message can name the argument the agent actually passed.
func (c *APIClient) discoverMonitorsReconcile(ctx context.Context, rawSources string) (*mcp.CallToolResult, error) {
	const shapeHint = "sources must be a JSON array, e.g. " +
		`[{"source_kind":"github-actions","source_ref":".github/workflows/nightly.yml","schedule_cron":"0 3 * * *"}]` +
		". Send [] to report that the scan found nothing."

	// Entries are forwarded RAW, for the same reason the response is: decoding
	// into a struct and re-marshalling would silently drop every field the API's
	// discoverySource grows that this package does not happen to know about.
	// The two are 6/6 today, and this file already argues that case for the
	// response — applying the opposite pattern to the request would leave the
	// inbound half exposed to exactly the drift the outbound half guards
	// against, and it is precisely how the failure_inbox_how_to mirror lost a
	// field once already.
	//
	// json.RawMessage still validates: Unmarshal rejects anything that is not a
	// JSON array, and a *pointer* to the slice is what distinguishes an absent
	// or null value from an empty scan. Shape is checked; contents are not
	// interpreted, which is the whole contract of a proxy.
	var sources *[]json.RawMessage
	if uErr := json.Unmarshal([]byte(strings.TrimSpace(rawSources)), &sources); uErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("%s Got: %v", shapeHint, uErr)), nil
	}
	if sources == nil {
		return mcp.NewToolResultError(shapeHint +
			" A JSON null is not a scan result: it cannot be told apart from a scanner that produced nothing, " +
			"and forwarding it would report every discovered monitor as orphaned."), nil
	}

	data, err := json.Marshal(map[string]any{"sources": *sources})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode sources: %v", err)), nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/discovery/reconcile", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Reconcile's rejections carry a `fix` that names the exact remedy —
		// TIMEZONE_REQUIRED's spells out the timedatectl command, and both the
		// cap and the concurrency conflict state that NOTHING was created.
		// c.problem drops that field, and an agent that only reads "tz is
		// required" has been handed the rule without the way to satisfy it.
		return mcp.NewToolResultError(reconcileProblemText(resp)), nil
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxReconcileResponseBytes))
	if readErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %v", readErr)), nil
	}

	// Counted for the summary line only. The BODY forwarded to the agent is
	// the API's own bytes, re-indented — decoding into a struct and
	// re-marshalling it would silently drop every monitor field this
	// package does not happen to know about, and the monitor DTO is where the
	// agent finds ping_url, source_kind and everything else it needs next.
	var counts struct {
		Created  []json.RawMessage `json:"created"`
		Existing []json.RawMessage `json:"existing"`
		Orphaned []json.RawMessage `json:"orphaned"`
	}
	if uErr := json.Unmarshal(body, &counts); uErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", uErr)), nil
	}

	var pretty bytes.Buffer
	if iErr := json.Indent(&pretty, body, "", "  "); iErr != nil {
		pretty.Reset()
		pretty.Write(body)
	}

	// The summary restates what was NOT done, because that is the half an
	// agent is most likely to assume wrongly: an orphaned monitor is still
	// live, and an existing one still holds whatever the user tuned by hand.
	summary := fmt.Sprintf(
		"Reconcile complete: %d created, %d already monitored, %d orphaned.\n"+
			"Nothing was deleted, paused or modified — the %d existing monitors were left exactly as they were, and the %d orphaned "+
			"monitors are still running and still alerting. Orphaned means this scan did not report their source; ask the user whether "+
			"those jobs are really gone before deleting anything.\n%s",
		len(counts.Created), len(counts.Existing), len(counts.Orphaned),
		len(counts.Existing), len(counts.Orphaned), pretty.String())

	return mcp.NewToolResultText(summary), nil
}
