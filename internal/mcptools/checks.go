package mcptools

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

// Check mirrors the relevant fields of the LastPing Check resource.
type Check struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Status       string   `json:"status"`
	ScheduleKind string   `json:"schedule_kind"`
	PeriodS      int64    `json:"period_s"`
	CronExpr     string   `json:"cron_expr"`
	TZ           string   `json:"tz"`
	GraceS       int64    `json:"grace_s"`
	Paused       bool     `json:"paused"`
	PingURL      string   `json:"ping_url"`
	MonitorType  string   `json:"monitor_type"`
	CreatedAt    string   `json:"created_at"`
	LastPingAt   *string  `json:"last_ping_at"`
	DueAt        *string  `json:"due_at"`
	Tags         []string `json:"tags"`
	// MaxRuntimeS mirrors the API DTO: a nil pointer means "unset", and the
	// overrun deadline falls back to grace_s. Decoded so get_monitor and
	// list_monitors show an agent the value it just wrote, rather than
	// silently dropping it on the floor.
	MaxRuntimeS *int64 `json:"max_runtime_s,omitempty"`
	// FailureThreshold is always present on the wire (the column is NOT NULL
	// DEFAULT 1), so it is not omitempty — an agent reading back a monitor
	// should see 1 rather than nothing.
	FailureThreshold int64 `json:"failure_threshold"`
	// StepTimeoutS mirrors max_runtime_s: nil means "stall detection is off",
	// which is what every monitor created before the field existed carries.
	// Decoded so an agent can read back what it just wrote and so
	// get_ping_instructions can tell it whether steps are actually armed.
	StepTimeoutS *int64 `json:"step_timeout_s,omitempty"`
	// BlockedTimeoutS mirrors max_runtime_s/step_timeout_s: nil means "falls
	// back to the 24h default", NOT "waits forever". Decoded so an agent
	// reading back a monitor sees the value it just wrote rather than
	// silently losing it.
	BlockedTimeoutS *int64 `json:"blocked_timeout_s,omitempty"`
	// ExpectEveryS is the silence floor. nil means "no floor", which for an
	// on_demand monitor means nothing at all is armed between runs. Decoded so
	// an agent can read back whether the monitor it is about to trust actually
	// has an absence deadline.
	ExpectEveryS *int64 `json:"expect_every_s,omitempty"`
	// NotifyMinRunS is the notification duration floor: a run shorter than
	// this many seconds does not produce an info-class notification (success,
	// started, every-run, note). nil means "no floor", which is every monitor
	// that predates the column. It NEVER suppresses a failure, a recovery, or
	// a blocked incident — see notifyMinRunDesc for the full semantics.
	// Decoded so an agent can read back whether the monitor it is about to
	// trust actually has a floor configured.
	NotifyMinRunS *int64 `json:"notify_min_run_s,omitempty"`
	// Assertions is NOT part of the /api/v1/checks payload — assertions are a
	// sub-resource with their own GET/PUT endpoint. It is decoded here anyway
	// so getMonitor can splice the second call's result into a single object
	// for the agent: a monitor whose output assertions are invisible on the
	// read path is a monitor an agent will happily "fix" by writing a set that
	// silently replaces the one already there. Absent on list_monitors (which
	// makes no per-monitor second call) and omitted when the set is empty.
	Assertions []MonitorAssertion `json:"assertions,omitempty"`
	// Guards is the monitor's metric-guard set, spliced in by getMonitor for
	// exactly the reason Assertions is: guards are a sub-resource, the write is
	// replace-the-set, and an agent that cannot see the current set will
	// happily destroy it. Absent on list_monitors and omitted when empty.
	Guards []MonitorGuard `json:"guards,omitempty"`
	// Routes is the monitor's alert routing, spliced in by getMonitor for the
	// third instance of the same hazard: set_route REPLACES the whole channel
	// set for one event type, and without this field there was no read path
	// for routing at all — an agent adding a destination to the 'down' event
	// had to pass every id blind, so the only safe-looking call it could make
	// silently deleted every route it had not been told about. Absent on
	// list_monitors and omitted when the monitor has no routes.
	Routes []Route `json:"routes,omitempty"`

	// MonitorFrom is the dormancy start: no deadline is computed before it.
	// nil means "armed immediately".
	MonitorFrom *string `json:"monitor_from,omitempty"`
	// RunawayCeiling is the pings/hour cap; nil means the runaway rule is off.
	RunawayCeiling *int64 `json:"runaway_ceiling,omitempty"`

	// HTTP probe configuration. All omitempty: they are absent on every
	// non-http monitor, and cluttering a heartbeat monitor's read with eight
	// zero-valued probe fields would make the real configuration harder to see.
	ProbeURL             string `json:"probe_url,omitempty"`
	ProbeMethod          string `json:"probe_method,omitempty"`
	ProbeIntervalS       int64  `json:"probe_interval_s,omitempty"`
	ProbeExpectedStatus  int64  `json:"probe_expected_status,omitempty"`
	ProbeExpectedBody    string `json:"probe_expected_body,omitempty"`
	ProbeTimeoutS        int64  `json:"probe_timeout_s,omitempty"`
	ProbeFollowRedirects bool   `json:"probe_follow_redirects,omitempty"`

	// CI binding. CiProvider/CiConfigured/CiWebhookURL are non-secret and are
	// returned on every read.
	CiProvider   string `json:"ci_provider,omitempty"`
	CiWorkflow   string `json:"ci_workflow,omitempty"`
	CiBranch     string `json:"ci_branch,omitempty"`
	CiConfigured bool   `json:"ci_configured,omitempty"`
	CiWebhookURL string `json:"ci_webhook_url,omitempty"`
	// CiSecret is WRITE-ONCE. The API returns it only in the 201 body of a
	// create that set ci_provider (and from the regenerate endpoint, which MCP
	// deliberately does not expose) and NEVER on a GET or list — so decoding it
	// here cannot leak it onto a read path: there is nothing on the wire to
	// decode. It is decoded at all because without it an agent could bind a
	// monitor to CI through MCP and then have no way to obtain the secret the
	// binding requires, which would make the capability useless rather than
	// merely absent. createMonitor surfaces it once; nothing else reads it.
	CiSecret string `json:"ci_secret,omitempty"`
}

// maxRuntimeClearSentinel is the value an agent passes to update_monitor to
// clear max_runtime_s. The MCP number schema cannot express JSON null, and 0 is
// never a legal max_runtime_s (the API floor is 60), so it cannot collide with
// a real value. updateMonitor translates it to an explicit null in the
// merge-patch body — without this the field would be set-once via MCP.
const maxRuntimeClearSentinel = 0

// stepTimeoutClearSentinel is the same trick for step_timeout_s: 0 is below the
// API floor of 10, so it can never be a real value, and it is the only way to
// send an explicit null through a numeric MCP argument. Kept as its own
// constant rather than shared with maxRuntimeClearSentinel because the two
// fields have different floors and only the floor makes the sentinel safe.
const stepTimeoutClearSentinel = 0

// blockedTimeoutClearSentinel is the same trick for blocked_timeout_s. Unlike
// max_runtime_s/step_timeout_s, blocked_timeout_s has no API-enforced floor —
// but the API already treats zero-or-negative as "unset" and falls back to
// its default, so a stored 0 and a stored NULL are behaviourally identical
// and 0 can never be a value worth preserving. Kept as its own constant
// rather than shared with the other two so a change to either field's floor
// cannot silently change this one too.
const blockedTimeoutClearSentinel = 0

// expectEveryClearSentinel is the same trick for expect_every_s: 0 is below the
// API floor of 60, so it can never be a real value. Its own constant, like the
// three above, because only that field's own floor is what makes its sentinel
// safe -- sharing one would couple three unrelated bounds.
const expectEveryClearSentinel = 0

// notifyMinRunClearSentinel is the same trick for notify_min_run_s: 0 is
// below the API floor of 60, so it can never be a real value. Its own
// constant, like the four above, because only this field's own floor is what
// makes its sentinel safe.
const notifyMinRunClearSentinel = 0

// runawayCeilingClearSentinel is the same trick for runaway_ceiling: 0 is never
// a legal ceiling — a monitor permitted zero pings per hour would sit in a
// runaway incident forever — so it cannot collide with a real value. Its own
// constant, like the four above, because only this field's own impossibility
// of zero is what makes its sentinel safe.
const runawayCeilingClearSentinel = 0

// detectionDescriptions are shared between create_monitor and update_monitor so
// the two tools cannot drift into describing the same field differently.
const (
	failureThresholdDesc = "Number of consecutive failures required before an incident opens. Default 1 (open on the very first failure). " +
		"This is how you stop a single transient blip from paging someone: set 2-5 on a job that fails occasionally for reasons that resolve themselves, " +
		"and no incident opens until that many runs in a row have failed. Any success resets the count to zero. " +
		"It gates the 'fail' cause ONLY — silence (a missed ping), overrun, never_started and runaway are time- or rate-based, " +
		"so a consecutive count means nothing for them and they are never delayed by it. Range 1-100."

	maxRuntimeDesc = "Maximum seconds a single run may take before it is reported overdue (the 'overrun' rule), measured from the run's start ping. " +
		"Omit to fall back to grace_s. This is how a long job avoids being flagged overdue while still being detected quickly if it goes silent: " +
		"e.g. grace_s=600 with max_runtime_s=14400 alerts 10 minutes after a missed ping but tolerates a 4-hour run. " +
		"It replaces grace_s for the overrun deadline ONLY — the silence rule and the first-run deadline still use grace_s. Range 60-31536000. " +
		"Not supported on http monitors: a probe has no start/success pair, so the overrun rule can never fire and the API returns 400 MAX_RUNTIME_NOT_SUPPORTED " +
		"(use probe_timeout_s to bound a single probe)."

	// agentIDDesc is shared between create_monitor and update_monitor so the
	// explicit-attachment rule cannot drift into two different wordings.
	agentIDDesc = "Attach this monitor to an agent from the registry, by the agent's id OR its slug (both are returned by register_agent). " +
		"Omit for a monitor with no owning agent. Naming an agent that does not exist is an error — 400 UNKNOWN_AGENT — " +
		"it is NEVER created implicitly; call register_agent first to get a valid agent_id."

	stepTimeoutDesc = "Progress budget in seconds: how long an armed run may go without reporting a step before a 'stalled' incident opens (the stall rule). " +
		"The clock is anchored on the LATER of the run's start ping and its most recent step, so a run that wedges before its first step is caught too. " +
		"Reach for this when 'still running' and 'still making progress' are different things — a long agent loop, a multi-stage pipeline, a migration. " +
		"max_runtime_s alone tells you nothing until the whole budget expires; step_timeout_s=300 on a 4-hour budget tells you within five minutes, and names the last step that reported. " +
		"To use it the run must report steps: call get_ping_instructions and use curl_step (POST <ping_url>/step?rid=<run-id>&step=<name>). " +
		"A monitor with step_timeout_s set whose job never reports a step will open a stalled incident on EVERY run — set the field and instrument the job in the same change. " +
		"Default: unset, which disables stall detection entirely; a monitor that sets nothing behaves exactly as it did before this field existed. Range 10-86400. " +
		"Two constraints. (1) It must be strictly LESS than the effective run budget, COALESCE(max_runtime_s, grace_s), or the API returns 400 STEP_TIMEOUT_EXCEEDS_BUDGET — " +
		"at or above the budget the run overruns first, so the stall rule could never fire. (2) Not supported on http monitors: a probe never arms a run and has no /step endpoint to call, " +
		"so the API returns 400 STEP_TIMEOUT_NOT_SUPPORTED. " +
		"A step resets the stall clock ONLY — it never extends max_runtime_s, so an agent that reports progress forever still overruns."

	// blockedTimeoutDesc is shared between create_monitor and update_monitor for
	// the same reason as the other detection descriptions above: the fallback
	// value is a fact an agent must get right, not something worth risking two
	// different wordings of.
	blockedTimeoutDesc = "Maximum seconds a run may sit in the 'blocked' state (an agent reported it is waiting on a human) before a 'blocked' incident opens. " +
		"UNSET DOES NOT MEAN WAIT FOREVER: omitting this does not disable the timeout, it falls back to the default, which is 24 HOURS — an agent " +
		"still blocked 24 hours after reporting so, with this field never set, gets a 'blocked' incident regardless. Lower it to be paged sooner when a stuck " +
		"approval is urgent; raise it for work that legitimately waits on a human for longer than a day. This is distinct from the immediate, non-incident " +
		"'blocked' notification a route on the 'blocked' event type delivers the moment the agent reports it (see set_route) — that fires right away; this field " +
		"governs the separate incident that opens only if the wait outlives the timeout. Accepted on every monitor_type: unlike max_runtime_s/step_timeout_s it has " +
		"no run-scoped precondition an http monitor could fail, so there is nothing to reject."

	expectEveryDesc = "SILENCE FLOOR in seconds: open a 'silence' incident if NO ping of any kind — success, start, fail, step — has arrived within this window, " +
		"regardless of the schedule. It is anchored on the monitor's last activity, not on a cadence, which is what makes it the ONLY absence rule an 'on_demand' " +
		"monitor can have: that schedule_kind arms nothing between runs, so without this field an on_demand monitor reads 'up' forever no matter how long the agent " +
		"stays dark. Set it on any on_demand agent monitor you would be alarmed to find silent — that is what it is for. " +
		"It does NOT fire mid-run: while a run is in flight (a start ping is outstanding) the floor stands down entirely and the run clock owns detection " +
		"(max_runtime_s, step_timeout_s), so a legitimate 4-hour run that reports nothing is still not an incident. A 'blocked' ping also pauses it, bounded by " +
		"blocked_timeout_s. On 'simple'/'cron' monitors it is a backstop rather than the main rule: it joins the existing deadline as whichever is SOONER, so it " +
		"can tighten detection under a long cadence (a daily cron has a ~25-hour blind window) but can never loosen it. " +
		"Default: unset, which means no floor and is exactly how every monitor behaved before this field existed. Range 60-31536000. " +
		"Accepted on every monitor_type and every schedule_kind."

	notifyMinRunDesc = "NOTIFICATION DURATION FLOOR in seconds: a run SHORTER than this does not produce an INFO-CLASS notification (success, started, every-run, note). " +
		"This exists for exactly one problem: on an agent monitor, one run is one task you asked for, so asking the agent 'what's 2+2' produces a start and a success " +
		"notification exactly like a 56-minute deploy does. If you have routed success/started/every-run/note to a destination, you WILL be paged for trivial runs " +
		"unless you set this. " +
		"IT NEVER SUPPRESSES A FAILURE. down, fail, recovery and blocked are alert-class and are never affected by this field, however short the run — a run that " +
		"failed in two seconds is exactly what you need to hear about, and this field cannot silence that, structurally, no matter how it is set. " +
		"It also never suppresses 'started': a run's duration does not exist yet the moment it begins, so started is always reported regardless of this floor. " +
		"And it never suppresses an event whose duration could not be measured at all (e.g. a bare success with no preceding start ping) — an unknown duration " +
		"always means 'notify', never 'suppress'. " +
		"Default: unset, which means no floor and is exactly how every monitor behaved before this field existed. Range 60-31536000. " +
		"Not supported on http monitors: an http probe has no start/success pair, so its run duration is never measured and the floor could never apply " +
		"(the API returns 400 NOTIFY_MIN_RUN_NOT_SUPPORTED)."

	// runawayCeilingDesc and monitorFromDesc are shared for the same
	// anti-drift reason as the detection descriptions above.
	runawayCeilingDesc = "PING-RATE CEILING: the maximum number of pings this monitor may receive in a rolling one-hour window. Exceeding it opens a 'runaway' incident. " +
		"This is the rule that catches a job or agent stuck in a LOOP — the failure every other rule misses, because a looping agent is pinging enthusiastically and " +
		"therefore reads 'up' the whole time it is burning tokens or money. Set it a little above the monitor's real cadence: a job that runs every 15 minutes sends about " +
		"4 pings/hour, so 20 absorbs retries and still catches a loop. It is RATE-based, so failure_threshold does not gate it and neither does any run budget. " +
		"Default: unset, which disables the runaway rule entirely."

	monitorFromDesc = "DORMANT UNTIL: an RFC 3339 timestamp before which no deadline is computed and no incident can open — the monitor is fully configured but not yet armed. " +
		"Use it when you provision ahead of the work: a monitor for a job that does not start running until next Monday is otherwise 'late' from the moment you create it, " +
		"which is a false alert on day one. The first-run deadline is seeded as monitor_from + grace_s. " +
		"Default: unset, meaning deadlines start immediately. Example: '2026-01-01T00:00:00Z'."

	// CI binding descriptions. ci_provider is create-only (immutable on PATCH),
	// so only ci_workflow/ci_branch are shared with update_monitor.
	ciProviderDesc = "Bind this monitor to a CI system, so the CI system itself reports every run by webhook and the job needs NO ping code at all. " +
		"One of: 'github', 'gitlab', 'jenkins'. " +
		"SET-ONCE: ci_provider can only be chosen when the monitor is created — update_monitor cannot change or remove it, so a monitor bound to the wrong provider must be deleted and recreated. " +
		"Setting it generates a webhook secret that is returned exactly ONCE, in THIS call's response, together with the webhook URL. It is never retrievable afterwards — " +
		"no MCP tool and no API read returns it again — so copy both out of the response and configure the CI webhook before doing anything else. " +
		"Omit for a monitor that pings for itself. Also set ci_workflow and ci_branch unless the repository really has exactly one workflow on one branch."

	ciWorkflowDesc = "CI filter: only count runs of the workflow / pipeline / job with this exact name. Requires ci_provider. " +
		"WITHOUT IT, EVERY workflow in the repository reports to this monitor — so one unrelated failing workflow opens an incident against a job that is perfectly healthy, " +
		"and a green run of a different workflow clears an incident the real job never recovered from. Set it whenever the repository has more than one workflow."

	ciBranchDesc = "CI filter: only count runs on this branch, e.g. 'main'. Requires ci_provider. " +
		"WITHOUT IT a run on ANY branch — a feature branch, a fork's pull request — reports to this monitor, so somebody else's broken branch marks your monitor down. " +
		"Set it to the branch whose health you actually care about, which is almost always the default branch."

	// HTTP probe descriptions. Shared between create_monitor and
	// update_monitor: an http monitor whose 'healthy' definition is described
	// one way on create and another on update is exactly the drift the other
	// shared constants in this block exist to prevent.
	probeURLDesc = "http monitors only: the absolute http/https URL to probe. Required when monitor_type='http'. " +
		"The host is resolved at write time and rejected if it resolves only to private/link-local addresses."

	probeIntervalDesc = "http monitors only: how often to probe, in seconds. Required when monitor_type='http'. Range 30-86400."

	probeMethodDesc = "http monitors only: the HTTP method the probe sends. One of 'GET', 'HEAD', 'POST'. Default 'GET'. " +
		"Use 'HEAD' for a cheap liveness check when the body does not matter — but note it returns no body, so probe_expected_body cannot match anything."

	probeExpectedStatusDesc = "http monitors only: the EXACT HTTP status code that counts as healthy. Default 200; any other code fails the probe. " +
		"Set it when the healthy answer is not 200 — 204 for a no-content health endpoint, or 301 when what you are checking is that a redirect still exists " +
		"(pair that with probe_follow_redirects=false, or the probe will follow it and see the destination's status instead)."

	probeExpectedBodyDesc = "http monitors only: a substring that MUST appear in the response body for the probe to count as healthy. " +
		"THIS IS THE DIFFERENCE BETWEEN 'the server answered' AND 'the app works': a broken app that renders an error page still returns 200, passes a status-only check, " +
		"and leaves the monitor green. Match on something only a healthy response contains, e.g. '\"status\":\"ok\"'. Substring match, not a regex, and case-sensitive. " +
		"Default: empty, meaning the body is not inspected at all."

	probeTimeoutDesc = "http monitors only: how many seconds a single probe may take before it counts as a failure. Range 1-30, default 10. " +
		"This is the http equivalent of max_runtime_s, which http monitors reject: it is the only way to say 'answering, but far too slowly to be healthy'."

	probeFollowRedirectsDesc = "http monitors only: whether the probe follows 3xx redirects. Default false. " +
		"Leaving it false is usually what you want: the redirect itself is then compared against probe_expected_status like any other response, so a site that starts " +
		"redirecting to a login wall, a parking page or an outage notice is CAUGHT rather than silently followed to a healthy-looking 200. " +
		"Set true only when the URL you are checking is legitimately a redirect to the thing you actually care about."

	// onDemandTradeoffDesc is shared between create_monitor and update_monitor
	// so the trade-off an agent is choosing cannot drift into two different
	// explanations depending on which tool it called.
	onDemandTradeoffDesc = "'on_demand' means no cadence at all: no period_s, no cron_expr — the API returns 400 if either is supplied — and, by default, NO ABSENCE " +
		"DEADLINES ARE ARMED BETWEEN RUNS. What this trades away: nothing tells you if the agent is never invoked again; silence between runs is invisible unless you " +
		"opt in to expect_every_s. " +
		"What it buys: a healthy agent that nobody happens to invoke for a week never generates a false 'late' or 'down' for simply not having been asked to run. " +
		"Only run-scoped detection still applies once a run starts — max_runtime_s (overrun), step_timeout_s (stall), blocked_timeout_s (stuck on a human) — because " +
		"those are anchored to a run's own start ping, not to a cadence. " +
		"IMPORTANT: if you would be alarmed to find this agent silent for hours, set expect_every_s as well — it is the silence floor, and it is the only thing that " +
		"makes an on_demand monitor detect absence at all. Choose 'simple'/'cron' when the agent is supposed to run on a cadence; choose " +
		"'on_demand' when invocation is inherently irregular and a quiet stretch between runs is expected, not a symptom."
)

func registerCheckTools(s *server.MCPServer) {
	// create_monitor
	s.AddTool(
		mcp.NewTool("create_monitor",
			mcp.WithDescription("Create a new LastPing monitor (or update an existing one if slug matches — returns 'updated' note on upsert). "+
				"For heartbeat/ci monitors supply schedule_kind ('simple' requires period_s, 'cron' requires cron_expr, 'on_demand' requires neither). "+
				"For http monitors supply probe_url and probe_interval_s instead — and set probe_expected_status/probe_expected_body too, because those are what define 'healthy'; "+
				"a probe with neither only proves something answered. "+
				"For a monitor fed by CI rather than by its own pings, set ci_provider here: it is the ONLY place it can be set, and the secret it returns is shown exactly once."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name, e.g. 'Daily backup job'.")),
			mcp.WithString("slug", mcp.Description("Optional stable ID. If a monitor with this slug exists, it will be updated (upsert). "+
				"Trimmed and lowercased automatically. Must match ^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$ (3-50 chars, lowercase alphanumeric and hyphens, "+
				"starting and ending alphanumeric) after normalisation. UUID-shaped slugs are rejected — they would be ambiguous with a monitor id "+
				"when importing into Terraform. Omit entirely for no slug.")),
			mcp.WithString("monitor_type", mcp.Description("'heartbeat' (default), 'ci', or 'http'.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple' (requires period_s), 'cron' (requires cron_expr), or 'on_demand' (requires neither). "+
				"Required for heartbeat/ci monitors. "+onDemandTradeoffDesc)),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds. Required when schedule_kind='simple'.")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression, e.g. '0 3 * * *'. Required when schedule_kind='cron'.")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation. Defaults to UTC.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds after a ping is due before alerting.")),
			mcp.WithNumber("failure_threshold", mcp.Description(failureThresholdDesc+
				" On an upsert (existing slug), omitting this resets the monitor's threshold to 1 — pass the current value to keep it.")),
			mcp.WithNumber("max_runtime_s", mcp.Description(maxRuntimeDesc+
				" On an upsert (existing slug), omitting this clears the monitor's max_runtime_s — pass the current value to keep it.")),
			mcp.WithNumber("step_timeout_s", mcp.Description(stepTimeoutDesc+
				" On an upsert (existing slug), omitting this clears the monitor's step_timeout_s and turns stall detection back off — pass the current value to keep it.")),
			mcp.WithNumber("blocked_timeout_s", mcp.Description(blockedTimeoutDesc+
				" On an upsert (existing slug), omitting this clears the monitor's blocked_timeout_s and falls back to the 24h default — pass the current value to keep it.")),
			mcp.WithNumber("expect_every_s", mcp.Description(expectEveryDesc+
				" On an upsert (existing slug), omitting this clears the monitor's expect_every_s and turns the silence floor back off — pass the current value to keep it.")),
			mcp.WithNumber("notify_min_run_s", mcp.Description(notifyMinRunDesc+
				" On an upsert (existing slug), omitting this clears the monitor's notify_min_run_s and turns the notification duration floor back off — pass the current value to keep it.")),
			mcp.WithNumber("runaway_ceiling", mcp.Description(runawayCeilingDesc+
				" On an upsert (existing slug), omitting this clears the monitor's ceiling and turns the runaway rule back off — pass the current value to keep it.")),
			mcp.WithString("monitor_from", mcp.Description(monitorFromDesc+
				" On an upsert (existing slug), omitting this clears the monitor's monitor_from and arms it immediately — pass the current value to keep it.")),
			mcp.WithString("probe_url", mcp.Description(probeURLDesc)),
			mcp.WithNumber("probe_interval_s", mcp.Description(probeIntervalDesc)),
			mcp.WithString("probe_method", mcp.Description(probeMethodDesc)),
			mcp.WithNumber("probe_expected_status", mcp.Description(probeExpectedStatusDesc)),
			mcp.WithString("probe_expected_body", mcp.Description(probeExpectedBodyDesc)),
			mcp.WithNumber("probe_timeout_s", mcp.Description(probeTimeoutDesc)),
			mcp.WithBoolean("probe_follow_redirects", mcp.Description(probeFollowRedirectsDesc)),
			mcp.WithString("ci_provider", mcp.Description(ciProviderDesc)),
			mcp.WithString("ci_workflow", mcp.Description(ciWorkflowDesc)),
			mcp.WithString("ci_branch", mcp.Description(ciBranchDesc)),
			mcp.WithString("tags", mcp.Description("Comma-separated labels for namespace scoping, e.g. 'agent:claude,env:prod'. Max 20 tags, each max 50 chars.")),
			mcp.WithString("agent_id", mcp.Description(agentIDDesc+
				" On an upsert (existing slug), omitting this leaves the monitor's current attachment (or lack of one) unchanged; supplying it re-applies the attachment, "+
				"so an agent re-running its own registration converges to 'attached' every time rather than silently no-opping after the first call.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createMonitor(ctx, req)
		},
	)

	// list_monitors
	s.AddTool(
		mcp.NewTool("list_monitors",
			mcp.WithDescription("List all monitors in the authenticated LastPing project. Returns id, name, slug, status, ping_url for each. Use the tag param to filter by a single tag."),
			mcp.WithString("tag", mcp.Description("Optional tag to filter by, e.g. 'agent:claude'. Returns only monitors that have this tag.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listMonitors(ctx, req)
		},
	)

	// get_monitor
	s.AddTool(
		mcp.NewTool("get_monitor",
			mcp.WithDescription("Get a single LastPing monitor by UUID. Returns the monitor's full configuration including its output assertions "+
				"(the `assertions` field: conditions a successful run's ping body must satisfy; absent when the monitor has none) and its metric "+
				"guards (the `guards` field: ceilings on a number the job reports about itself; absent when the monitor has none) and its alert "+
				"ROUTING (the `routes` field: which destinations receive which event type; absent when the monitor has none). "+
				"Read this before calling update_monitor with assertions or guards, and before calling set_route — every one of those three writes REPLACES a whole set, "+
				"so an agent that did not read the current one first will silently drop assertions, guards or destinations somebody else configured."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getMonitor(ctx, id)
		},
	)

	// update_monitor
	s.AddTool(
		mcp.NewTool("update_monitor",
			mcp.WithDescription("Update an existing LastPing monitor's schedule/config by UUID using merge-patch semantics: "+
				"only the fields you supply are changed, and any field you omit keeps its current stored value. "+
				"If supplied, tags replaces the full tag set on the monitor (not merged). slug is immutable and cannot be changed. "+
				"This is also the tool that sets a monitor's OUTPUT ASSERTIONS (the assertions argument) — conditions the ping body of a successful "+
				"run must satisfy, which is how a job that exits zero having done nothing gets caught — and its METRIC GUARDS (the guards argument) — "+
				"ceilings on a number the job reports, which is how an agent that loops and burns money gets caught. "+
				"Like tags, assertions and guards each REPLACE the full set. "+
				"ci_provider is NOT patchable — it is immutable once set, so only its ci_workflow/ci_branch filters can be changed here; "+
				"rebinding a monitor to a different CI system means deleting and recreating it."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple', 'cron', or 'on_demand'. "+onDemandTradeoffDesc)),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds (for schedule_kind='simple').")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression (for schedule_kind='cron').")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds.")),
			mcp.WithNumber("failure_threshold", mcp.Description(failureThresholdDesc+" Omit to leave the monitor's current threshold unchanged.")),
			mcp.WithNumber("max_runtime_s", mcp.Description(maxRuntimeDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and fall back to grace_s.")),
			mcp.WithNumber("step_timeout_s", mcp.Description(stepTimeoutDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and disable stall detection.")),
			mcp.WithNumber("blocked_timeout_s", mcp.Description(blockedTimeoutDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and fall back to the 24h default.")),
			mcp.WithNumber("expect_every_s", mcp.Description(expectEveryDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and turn the silence floor off.")),
			mcp.WithNumber("notify_min_run_s", mcp.Description(notifyMinRunDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and turn the notification duration floor off.")),
			mcp.WithNumber("runaway_ceiling", mcp.Description(runawayCeilingDesc+
				" Omit to leave the monitor's current value unchanged; pass 0 to clear it and turn the runaway rule off.")),
			mcp.WithString("monitor_from", mcp.Description(monitorFromDesc+
				" Omit to leave the monitor's current value unchanged.")),
			mcp.WithString("probe_url", mcp.Description(probeURLDesc+" Omit to leave unchanged.")),
			mcp.WithNumber("probe_interval_s", mcp.Description(probeIntervalDesc+" Omit to leave unchanged.")),
			mcp.WithString("probe_method", mcp.Description(probeMethodDesc+" Omit to leave unchanged.")),
			mcp.WithNumber("probe_expected_status", mcp.Description(probeExpectedStatusDesc+" Omit to leave unchanged.")),
			mcp.WithString("probe_expected_body", mcp.Description(probeExpectedBodyDesc+
				" Omit to leave unchanged; pass an explicit JSON null to stop inspecting the body. An empty string leaves it unchanged, so it cannot be cleared that way.")),
			mcp.WithNumber("probe_timeout_s", mcp.Description(probeTimeoutDesc+" Omit to leave unchanged.")),
			mcp.WithBoolean("probe_follow_redirects", mcp.Description(probeFollowRedirectsDesc+" Omit to leave unchanged; pass false to turn following back off.")),
			mcp.WithString("ci_workflow", mcp.Description(ciWorkflowDesc+
				" Omit to leave the current filter unchanged; pass an explicit JSON null to remove it. An EMPTY STRING also leaves it unchanged — "+
				"that is a deliberate API compatibility rule, not a bug, so an empty string cannot be used to clear the filter.")),
			mcp.WithString("ci_branch", mcp.Description(ciBranchDesc+
				" Omit to leave the current filter unchanged; pass an explicit JSON null to remove it. An EMPTY STRING also leaves it unchanged — "+
				"that is a deliberate API compatibility rule, not a bug, so an empty string cannot be used to clear the filter.")),
			mcp.WithString("tags", mcp.Description("Comma-separated labels to set on this monitor, e.g. 'agent:claude,env:prod'. Replaces existing tags. Max 20 tags, each max 50 chars.")),
			mcp.WithString("agent_id", mcp.Description(agentIDDesc+" Omit to leave the monitor's current attachment (or lack of one) unchanged.")),
			mcp.WithString("assertions", mcp.Description(assertionsDesc)),
			mcp.WithString("guards", mcp.Description(guardsDesc)),
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
			return c.updateMonitor(ctx, id, req)
		},
	)

	// delete_monitor
	s.AddTool(
		mcp.NewTool("delete_monitor",
			mcp.WithDescription("Permanently delete a LastPing monitor by UUID. This cannot be undone."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.deleteMonitor(ctx, id)
		},
	)

	// pause_monitor
	s.AddTool(
		mcp.NewTool("pause_monitor",
			mcp.WithDescription("Pause a LastPing monitor so it stops alerting (paused=true). The monitor still receives pings but does not alert."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.simpleCheckPost(ctx, id, "pause")
		},
	)

	// resume_monitor
	s.AddTool(
		mcp.NewTool("resume_monitor",
			mcp.WithDescription("Resume a paused LastPing monitor (paused=false). Alerting resumes on the next missed ping."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID."))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.simpleCheckPost(ctx, id, "resume")
		},
	)

	// snooze_monitor
	s.AddTool(
		mcp.NewTool("snooze_monitor",
			mcp.WithDescription("Set or clear a maintenance window on a monitor. During the window the monitor will not alert. "+
				"Provide exactly one of: duration (e.g. '1h', '24h'), until (RFC 3339 timestamp), or clear=true to remove the window."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("duration", mcp.Description("Go duration string, e.g. '1h' or '24h'. Use this OR until OR clear.")),
			mcp.WithString("until", mcp.Description("RFC 3339 end timestamp. Use this OR duration OR clear.")),
			mcp.WithBoolean("clear", mcp.Description("Set true to remove the active maintenance window.")),
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
			return c.snoozeMonitor(ctx, id, req)
		},
	)
}

// splitTags parses a comma-separated tags string into a trimmed, deduplicated slice.
// Returns nil when the input string is empty (no tags key is included in the request body).
func splitTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, dup := seen[part]; !dup {
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

// --- APIClient methods for checks ---

func (c *APIClient) createMonitor(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	if v, ok := args["slug"].(string); ok && v != "" {
		body["slug"] = v
	}
	if v, ok := args["monitor_type"].(string); ok && v != "" {
		body["monitor_type"] = v
	}
	if v, ok := args["schedule_kind"].(string); ok && v != "" {
		body["schedule_kind"] = v
	}
	if v, ok := args["period_s"].(float64); ok && v > 0 {
		body["period_s"] = v
	}
	if v, ok := args["cron_expr"].(string); ok && v != "" {
		body["cron_expr"] = v
	}
	if v, ok := args["tz"].(string); ok && v != "" {
		body["tz"] = v
	}
	if v, ok := args["grace_s"].(float64); ok && v > 0 {
		body["grace_s"] = v
	}
	// POST /api/v1/checks is the slug-upsert path and has full-replace
	// semantics: a field omitted here is reset to its default on an existing
	// monitor. Both are therefore forwarded whenever supplied.
	if v, ok := args["failure_threshold"].(float64); ok && v > 0 {
		body["failure_threshold"] = v
	}
	if v, ok := args["max_runtime_s"].(float64); ok && v > 0 {
		body["max_runtime_s"] = v
	}
	if v, ok := args["step_timeout_s"].(float64); ok && v > 0 {
		body["step_timeout_s"] = v
	}
	if v, ok := args["blocked_timeout_s"].(float64); ok && v > 0 {
		body["blocked_timeout_s"] = v
	}
	if v, ok := args["expect_every_s"].(float64); ok && v > 0 {
		body["expect_every_s"] = v
	}
	if v, ok := args["notify_min_run_s"].(float64); ok && v > 0 {
		body["notify_min_run_s"] = v
	}
	if v, ok := args["runaway_ceiling"].(float64); ok && v > 0 {
		body["runaway_ceiling"] = v
	}
	if v, ok := args["monitor_from"].(string); ok && v != "" {
		body["monitor_from"] = v
	}
	if v, ok := args["probe_url"].(string); ok && v != "" {
		body["probe_url"] = v
	}
	if v, ok := args["probe_interval_s"].(float64); ok && v > 0 {
		body["probe_interval_s"] = v
	}
	if v, ok := args["probe_method"].(string); ok && v != "" {
		body["probe_method"] = v
	}
	if v, ok := args["probe_expected_status"].(float64); ok && v > 0 {
		body["probe_expected_status"] = v
	}
	if v, ok := args["probe_expected_body"].(string); ok && v != "" {
		body["probe_expected_body"] = v
	}
	if v, ok := args["probe_timeout_s"].(float64); ok && v > 0 {
		body["probe_timeout_s"] = v
	}
	// probe_follow_redirects is forwarded on PRESENCE, not on truth: false is
	// the default but it is also a deliberate choice, and the create path is
	// full-replace, so gating on `v == true` would make "follow redirects" a
	// setting an upsert could turn on but never turn back off.
	if v, ok := args["probe_follow_redirects"].(bool); ok {
		body["probe_follow_redirects"] = v
	}
	if v, ok := args["ci_provider"].(string); ok && v != "" {
		body["ci_provider"] = v
	}
	if v, ok := args["ci_workflow"].(string); ok && v != "" {
		body["ci_workflow"] = v
	}
	if v, ok := args["ci_branch"].(string); ok && v != "" {
		body["ci_branch"] = v
	}
	if v, ok := args["tags"].(string); ok {
		if tags := splitTags(v); tags != nil {
			body["tags"] = tags
		}
	}
	if v, ok := args["agent_id"].(string); ok && v != "" {
		body["agent_id"] = v
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	note := "created"
	if resp.StatusCode == http.StatusOK {
		note = "updated (upsert: existing monitor with this slug was updated)"
	}
	out := fmt.Sprintf("Monitor %s (id=%s, status=%s, ping_url=%s)", note, ch.ID, ch.Status, ch.PingURL)

	// The CI webhook secret exists only in this response. It is not on any read
	// path, and MCP exposes no regenerate tool, so a caller that does not act on
	// it here has bound a monitor to CI that can never receive a run — the
	// binding is not recoverable through this surface. Say so loudly rather
	// than appending it as one more field.
	if ch.CiSecret != "" {
		out += fmt.Sprintf("\n\nCI BINDING — THE SECRET BELOW IS SHOWN ONCE AND CANNOT BE RETRIEVED AGAIN.\n"+
			"Configure the CI webhook NOW, before doing anything else with this monitor.\n"+
			"  provider:    %s\n"+
			"  webhook URL: %s\n"+
			"  secret:      %s\n"+
			"If you lose it, no MCP tool can recover it: the secret must be regenerated from the dashboard or the REST API, "+
			"and until it is, this monitor receives nothing from CI.", ch.CiProvider, ch.CiWebhookURL, ch.CiSecret)
	}
	return mcp.NewToolResultText(out), nil
}

func (c *APIClient) listMonitors(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	u := c.BaseURL + "/api/v1/checks"
	args := req.GetArguments()
	if tagVal, ok := args["tag"].(string); ok && strings.TrimSpace(tagVal) != "" {
		u += "?tag=" + strings.TrimSpace(tagVal)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var checks []Check
	if err := json.NewDecoder(resp.Body).Decode(&checks); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(checks) == 0 {
		return mcp.NewToolResultText("No monitors found. Create one with create_monitor."), nil
	}

	out, _ := json.MarshalIndent(checks, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) getMonitor(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	// Assertions are a sub-resource, so reading them costs a second call. It is
	// made unconditionally rather than lazily: the whole point of surfacing them
	// on the read path is that an agent about to write a replacement set can see
	// what it is about to replace, and a field that appears only sometimes is one
	// an agent cannot rely on. A failure here is NOT fatal — the monitor itself
	// was fetched successfully, so the result degrades to "monitor without its
	// assertions, and a note saying so" instead of losing the read entirely.
	assertionsNote := ""
	if as, aErr := c.getAssertions(ctx, id); aErr != nil {
		assertionsNote = fmt.Sprintf("\n\nNote: the monitor's output assertions could not be read (%v). "+
			"Do NOT call update_monitor with an assertions argument until a get_monitor succeeds — "+
			"writing that argument replaces the whole set and would drop assertions you have not seen.", aErr)
	} else {
		ch.Assertions = as
	}

	// Metric guards are a second sub-resource on the same contract, read for
	// the same reason and degraded the same way.
	guardsNote := ""
	if gs, gErr := c.getGuards(ctx, id); gErr != nil {
		guardsNote = fmt.Sprintf("\n\nNote: the monitor's metric guards could not be read (%v). "+
			"Do NOT call update_monitor with a guards argument until a get_monitor succeeds — "+
			"writing that argument replaces the whole set and would drop guards you have not seen.", gErr)
	} else {
		ch.Guards = gs
	}

	// Alert routing is a third sub-resource with exactly the same hazard, and
	// it is spliced here rather than given its own tool for exactly the reason
	// the two above are: the danger is not "an agent cannot list routes", it is
	// "an agent writes a route without having read the current one". Putting
	// the read behind a separate tool leaves that call optional; putting it in
	// the payload of the tool an agent already calls before editing a monitor
	// means the current set is in front of it whether or not it thought to ask.
	routesNote := ""
	if rs, rErr := c.getRoutes(ctx, id); rErr != nil {
		routesNote = fmt.Sprintf("\n\nNote: the monitor's alert routes could not be read (%v). "+
			"Do NOT call set_route until a get_monitor succeeds — set_route REPLACES every destination for the event type it names, "+
			"so calling it without the current list would silently unroute destinations you have not seen.", rErr)
	} else {
		ch.Routes = rs
	}

	out, _ := json.MarshalIndent(ch, "", "  ")
	return mcp.NewToolResultText(string(out) + assertionsNote + guardsNote + routesNote), nil
}

func (c *APIClient) updateMonitor(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	if v, ok := args["schedule_kind"].(string); ok && v != "" {
		body["schedule_kind"] = v
	}
	if v, ok := args["period_s"].(float64); ok && v > 0 {
		body["period_s"] = v
	}
	if v, ok := args["cron_expr"].(string); ok && v != "" {
		body["cron_expr"] = v
	}
	if v, ok := args["tz"].(string); ok && v != "" {
		body["tz"] = v
	}
	if v, ok := args["grace_s"].(float64); ok && v > 0 {
		body["grace_s"] = v
	}
	if v, ok := args["failure_threshold"].(float64); ok && v > 0 {
		body["failure_threshold"] = v
	}
	// PATCH is RFC 7396 merge-patch: an omitted key keeps the stored value, an
	// explicit null clears it. The sentinel is the only way to say "clear" from
	// a numeric tool argument.
	if v, ok := args["max_runtime_s"].(float64); ok {
		switch {
		case v == maxRuntimeClearSentinel:
			body["max_runtime_s"] = nil
		case v > 0:
			body["max_runtime_s"] = v
		}
	}
	if v, ok := args["step_timeout_s"].(float64); ok {
		switch {
		case v == stepTimeoutClearSentinel:
			body["step_timeout_s"] = nil
		case v > 0:
			body["step_timeout_s"] = v
		}
	}
	if v, ok := args["blocked_timeout_s"].(float64); ok {
		switch {
		case v == blockedTimeoutClearSentinel:
			body["blocked_timeout_s"] = nil
		case v > 0:
			body["blocked_timeout_s"] = v
		}
	}
	if v, ok := args["expect_every_s"].(float64); ok {
		switch {
		case v == expectEveryClearSentinel:
			body["expect_every_s"] = nil
		case v > 0:
			body["expect_every_s"] = v
		}
	}
	if v, ok := args["notify_min_run_s"].(float64); ok {
		switch {
		case v == notifyMinRunClearSentinel:
			body["notify_min_run_s"] = nil
		case v > 0:
			body["notify_min_run_s"] = v
		}
	}
	// runaway_ceiling takes the same clear-sentinel treatment as the five
	// fields above. Its own constant for the same reason: 0 is never a legal
	// ceiling (a monitor allowed zero pings per hour would be permanently in a
	// runaway incident), and that is what makes the sentinel safe here.
	if v, ok := args["runaway_ceiling"].(float64); ok {
		switch {
		case v == runawayCeilingClearSentinel:
			body["runaway_ceiling"] = nil
		case v > 0:
			body["runaway_ceiling"] = v
		}
	}
	if v, ok := args["monitor_from"].(string); ok && v != "" {
		body["monitor_from"] = v
	}
	if v, ok := args["probe_url"].(string); ok && v != "" {
		body["probe_url"] = v
	}
	if v, ok := args["probe_interval_s"].(float64); ok && v > 0 {
		body["probe_interval_s"] = v
	}
	if v, ok := args["probe_method"].(string); ok && v != "" {
		body["probe_method"] = v
	}
	if v, ok := args["probe_expected_status"].(float64); ok && v > 0 {
		body["probe_expected_status"] = v
	}
	if v, ok := args["probe_timeout_s"].(float64); ok && v > 0 {
		body["probe_timeout_s"] = v
	}
	// Present-or-absent, not truthiness: on a PATCH, false means "stop
	// following redirects", which is a real change an agent must be able to
	// make. Gating on the value would make the setting one-way.
	if v, ok := args["probe_follow_redirects"].(bool); ok {
		body["probe_follow_redirects"] = v
	}
	// probe_expected_body, ci_workflow and ci_branch are the three string
	// fields whose EMPTY STRING the API deliberately reads as "leave as
	// stored" (a compatibility rule for clients that send a full body with ""
	// for an unset filter). An explicit JSON null is therefore the only way to
	// clear them, and a client that sends null for a string argument arrives
	// here as a present key with a nil value. Handling that shape is what
	// makes clearing expressible from MCP at all; without it these three
	// fields would be set-once through this surface.
	for _, key := range []string{"probe_expected_body", "ci_workflow", "ci_branch"} {
		raw, present := args[key]
		if !present {
			continue
		}
		if raw == nil {
			body[key] = nil
			continue
		}
		if v, ok := raw.(string); ok && v != "" {
			body[key] = v
		}
	}
	if v, ok := args["tags"].(string); ok {
		if tags := splitTags(v); tags != nil {
			body["tags"] = tags
		}
	}
	if v, ok := args["agent_id"].(string); ok && v != "" {
		body["agent_id"] = v
	}

	// Assertions live behind their own replace-the-set PUT, so they cannot ride
	// along in the merge-patch body. They are parsed BEFORE the PATCH is
	// issued so a JSON-syntax error costs nothing, instead of leaving the
	// monitor's schedule already patched and its assertions rejected. The
	// content itself (kind, path, the 20-per-monitor cap) is validated by the
	// API, not here.
	rawAssertions, _ := args["assertions"].(string)
	assertions, wantAssertions, aErr := parseAssertionsArg(rawAssertions)
	if aErr != nil {
		return mcp.NewToolResultError(aErr.Error()), nil
	}

	// Metric guards ride the same rails, and are parsed here for the same
	// reason.
	rawGuards, _ := args["guards"].(string)
	guards, wantGuards, gErr := parseGuardsArg(rawGuards)
	if gErr != nil {
		return mcp.NewToolResultError(gErr.Error()), nil
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/checks/"+id, bytes.NewReader(data))
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	// The PATCH landed. If the caller also asked for assertions, replace the set
	// now. A failure at this point is a PARTIAL update and is reported as one:
	// the fields are already saved and re-issuing the same call is safe, but an
	// agent told only "failed" would reasonably assume nothing changed.
	if wantAssertions {
		saved, putErr := c.putAssertions(ctx, id, assertions)
		if putErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"PARTIAL UPDATE: the monitor's fields were saved, but its assertions were NOT: %v. "+
					"The monitor's previous assertion set is still in place. Re-run the same update_monitor call to retry (it is safe to repeat).",
				putErr)), nil
		}
		ch.Assertions = saved
	}

	if wantGuards {
		saved, putErr := c.putGuards(ctx, id, guards)
		if putErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"PARTIAL UPDATE: the monitor's fields were saved, but its metric guards were NOT: %v. "+
					"The monitor's previous guard set is still in place. Re-run the same update_monitor call to retry (it is safe to repeat).",
				putErr)), nil
		}
		ch.Guards = saved
	}

	out, _ := json.MarshalIndent(ch, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("Monitor updated:\n%s", out)), nil
}

func (c *APIClient) deleteMonitor(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/checks/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Monitor %s deleted successfully.", id)), nil
}

// simpleCheckPost is used for pause and resume (no request body, returns a Check).
func (c *APIClient) simpleCheckPost(ctx context.Context, id, action string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks/"+id+"/"+action, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Monitor %sd. paused=%v, status=%s", action, ch.Paused, ch.Status)), nil
}

func (c *APIClient) snoozeMonitor(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["duration"].(string); ok && v != "" {
		body["duration"] = v
	}
	if v, ok := args["until"].(string); ok && v != "" {
		body["until"] = v
	}
	if v, ok := args["clear"].(bool); ok && v {
		body["clear"] = true
	}

	if len(body) == 0 {
		return mcp.NewToolResultError("Provide exactly one of: duration (e.g. '1h'), until (RFC 3339 timestamp), or clear=true."), nil
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/checks/"+id+"/snooze", bytes.NewReader(data))
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Check
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Maintenance window set on monitor %s (status=%s).", id, ch.Status)), nil
}
