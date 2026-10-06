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
	// Set together by discovery; both absent for a monitor a person made.
	SourceKind string `json:"source_kind,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
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

	// TraceContent is what this monitor's traces keep of prompt, command and
	// tool content: "dropped" (the default) or "redacted" (a person opted
	// in; secret-shaped values are redacted at ingest). Always present on
	// the wire.
	TraceContent string `json:"trace_content,omitempty"`

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
// the two tools cannot drift into describing the same field differently. They
// are declarative facts about the field (the directory policy forbids
// instructions aimed at the model); policy_test.go guards the wording.
const (
	failureThresholdDesc = "Consecutive failures required before an incident opens; default 1 (the first failure). " +
		"2-5 absorbs a job's occasional self-resolving failure. Any success resets the count. " +
		"It gates only the 'fail' cause: silence, overrun, never_started and runaway are time- or rate-based and never delayed by it. " +
		"A run ending on the model provider's API error (server_error, overloaded, rate_limit) neither counts nor resets it; " +
		"3 such runs in a row, or this many when higher, open an 'upstream' incident. Range 1-100."

	maxRuntimeDesc = "Maximum seconds one run may take, from its start ping, before it is reported overdue (the 'overrun' rule). Unset falls back to grace_s. " +
		"Example: grace_s=600 with max_runtime_s=14400 alerts 10 minutes after a missed ping but tolerates a 4-hour run. " +
		"It replaces grace_s for the overrun deadline only; the silence rule and the first-run deadline still use grace_s. Range 60-31536000. " +
		"Not supported on http monitors (400 MAX_RUNTIME_NOT_SUPPORTED); probe_timeout_s bounds a probe."

	// agentIDDesc is shared between create_monitor and update_monitor so the
	// explicit-attachment rule cannot drift into two different wordings.
	agentIDDesc = "Attaches the monitor to a registered agent, by the agent's id or slug (register_agent returns both). " +
		"An agent that does not exist is an error (400 UNKNOWN_AGENT); no agent is created implicitly."

	stepTimeoutDesc = "Seconds an armed run may go without a step before a 'stalled' incident opens, timed from the later of its start ping and its latest step. " +
		"Steps are POST <ping_url>/step?rid=<run-id>&step=<name> (curl_step in get_ping_instructions). " +
		"When set, a job that sends no steps opens a stalled incident on every run. " + stallNeedsStepsFact + " " +
		"Unset (default): no stall detection. Range 10-86400, strictly below COALESCE(max_runtime_s, grace_s) (400 STEP_TIMEOUT_EXCEEDS_BUDGET). " +
		"Not supported on http monitors (400 STEP_TIMEOUT_NOT_SUPPORTED). A step never extends max_runtime_s."

	// stallNeedsStepsFact states, as a fact about the tool, that stall
	// detection only works for a job that calls /step: the Claude Code hook
	// and the reporting prompt never send a step. Matches the hosted server at
	// mcp.lastping.dev.
	stallNeedsStepsFact = "The Claude Code hook and the reporting prompt send no steps."

	// blockedTimeoutDesc is shared between create_monitor and update_monitor:
	// the fallback value is a fact an agent has to get right.
	blockedTimeoutDesc = "Seconds a run may stay 'blocked' (an agent reported it is waiting on a human) before a 'blocked' incident opens. " +
		"Unset means the 24-hour default, not no limit. " +
		"Separate from the 'blocked' notification a route delivers (held 10 minutes, sent at most once per blocked stretch of a run). " +
		"Accepted on every monitor_type."

	expectEveryDesc = "Silence floor in seconds: a 'silence' incident opens when no ping of any kind (success, start, fail, step) arrives within this window, " +
		"timed from the monitor's last activity. It is the only absence rule an 'on_demand' monitor has. " +
		"It stands down while a run is in flight (the run clock owns that), and a 'blocked' ping pauses it up to blocked_timeout_s. " +
		"On 'simple'/'cron' monitors it joins the schedule deadline as whichever is sooner, so it can only tighten detection. " +
		"Unset (default): no floor. Range 60-31536000. Accepted on every monitor_type and schedule_kind."

	notifyMinRunDesc = "Notification duration floor in seconds: a run shorter than this sends no info-class notification (success, every-run, note), " +
		"so a trivial agent run does not notify a destination those events are routed to. " +
		"It never suppresses a failure: down, fail, recovery, blocked and started always notify, as does an event whose duration was not measured (such as a success with no start ping). " +
		"Unset (default): no floor. Range 60-31536000. Not supported on http monitors (400 NOTIFY_MIN_RUN_NOT_SUPPORTED)."

	// runawayCeilingDesc and monitorFromDesc are shared for the same
	// anti-drift reason as the detection descriptions above.
	runawayCeilingDesc = "Ping-rate ceiling: the most pings this monitor may receive in a rolling hour; exceeding it opens a 'runaway' incident. " +
		"It catches a job or agent stuck in a loop, which keeps pinging and otherwise reads 'up'. " +
		"A job every 15 minutes sends about 4 pings an hour, so 20 leaves room for retries. " +
		"Rate-based: failure_threshold and run budgets do not gate it. Unset (default): the rule is off."

	monitorFromDesc = "Dormant until: an RFC 3339 timestamp before which no deadline is computed and no incident opens, for a monitor provisioned ahead of its job. " +
		"The first-run deadline is monitor_from + grace_s. Unset (default): deadlines start immediately. Example: '2026-01-01T00:00:00Z'."

	// CI binding descriptions. ci_provider is create-only (immutable on PATCH),
	// so only ci_workflow/ci_branch are shared with update_monitor.
	ciProviderDesc = "Binds the monitor to a CI system ('github', 'gitlab' or 'jenkins'), whose webhook then reports every run with no ping code in the job. " +
		"Fixed at creation: update_monitor cannot change or remove it. " +
		"The webhook secret and URL appear only in this call's result; no tool or API read returns the secret again. " +
		"Not accepted on monitor_type='http' (400 FIELD_NOT_IN_SHAPE). ci_workflow and ci_branch narrow which runs count."

	ciWorkflowDesc = "CI filter: only runs of the workflow, pipeline or job with this exact name count. " +
		"Needs ci_provider: without a CI binding it is refused with 400 FIELD_NOT_IN_SHAPE (monitor_type='ci' alone binds nothing). " +
		"Exception: create_monitor on an existing slug (an upsert) never writes it, and with ci_provider in the same call it is accepted and discarded; " +
		"update_monitor persists it. Unset, every workflow in the repository reports to the monitor, so an unrelated workflow's failure opens an incident and its success clears one."

	ciBranchDesc = "CI filter: only runs on this branch count, e.g. 'main'. Needs ci_provider (400 FIELD_NOT_IN_SHAPE without a CI binding). " +
		"Same upsert exception as ci_workflow: create_monitor on an existing slug never writes it; update_monitor does. " +
		"Unset, a run on any branch, a fork's pull request included, reports to the monitor."

	// HTTP probe descriptions, shared between create_monitor and update_monitor.
	probeURLDesc = "http monitors only: the absolute http/https URL to probe. Required when monitor_type='http'. " +
		"The host is resolved at write time and rejected if it resolves only to private/link-local addresses."

	probeIntervalDesc = "http monitors only: how often to probe, in seconds. Required when monitor_type='http'. Range 30-86400."

	probeMethodDesc = "http monitors only: the HTTP method the probe sends: 'GET' (default), 'HEAD' or 'POST'. " +
		"'HEAD' returns no body, so probe_expected_body cannot match with it."

	probeExpectedStatusDesc = "http monitors only: the exact HTTP status that counts as healthy; default 200, any other code fails the probe. " +
		"E.g. 204 for a no-content endpoint, or 301 to check that a redirect still exists (with probe_follow_redirects=false; otherwise the probe sees the destination's status)."

	probeExpectedBodyDesc = "http monitors only: a substring the response body has to contain for the probe to pass, e.g. '\"status\":\"ok\"'. " +
		"A broken app's error page can still return 200 and pass a status-only check; this catches it. " +
		"Case-sensitive substring, not a regex. Default empty: the body is not inspected."

	probeTimeoutDesc = "http monitors only: seconds one probe may take before it counts as a failure. Range 1-30, default 10. " +
		"The http counterpart of max_runtime_s, which http monitors reject."

	probeFollowRedirectsDesc = "http monitors only: whether the probe follows 3xx redirects; default false. " +
		"When false, the redirect itself is compared with probe_expected_status, so a site that starts redirecting to a login wall or a parking page " +
		"fails the probe instead of passing on the destination's 200."

	// onDemandTradeoffDesc is shared between create_monitor and update_monitor
	// so the trade-off cannot drift into two explanations.
	onDemandTradeoffDesc = "'on_demand' has no cadence (period_s or cron_expr with it is a 400) and arms no absence deadline between runs: " +
		"an agent that is never invoked again raises nothing unless expect_every_s is set, and an idle healthy agent never raises a false 'late'. " +
		"Once a run starts, max_runtime_s, step_timeout_s and blocked_timeout_s still apply. " +
		"'simple'/'cron' fit work on a cadence; 'on_demand' fits irregular invocation."

	// onDemandGraceDefaultDesc is the sentence create_monitor's grace_s
	// carries about the default createMonitor fills in.
	onDemandGraceDefaultDesc = "Omitted on an on_demand monitor, LastPing uses 300 seconds; on_demand has no cadence, so grace only sets the first-run deadline and the overrun fallback."

	// traceContentDesc is shared by create_monitor and update_monitor: the
	// default and what each value stores read the same in both.
	traceContentDesc = "What this monitor's traces keep of prompt, command and tool content: 'dropped' (the default) removes it; " +
		"'redacted' stores prompt, command and tool content from the traced sessions, with secret-shaped values redacted on arrival."
)

// onDemandDefaultGraceS is the grace createMonitor sends for an on_demand
// monitor created without one (see onDemandGraceDefaultDesc).
const onDemandDefaultGraceS = 300

func registerCheckTools(s *server.MCPServer) {
	// create_monitor
	s.AddTool(
		newTool("create_monitor",
			mcp.WithDescription("Creates a monitor, or updates the existing one when slug matches (an upsert; the result says 'updated'). "+
				"Heartbeat and ci monitors take schedule_kind: 'simple' with period_s, 'cron' with cron_expr, or 'on_demand' with neither. "+
				"http monitors take probe_url and probe_interval_s; probe_expected_status and probe_expected_body define a healthy response, "+
				"and a probe with neither only checks that something answered. "+
				"ci_provider can be set only here, and the webhook secret it returns appears only in this result."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name, e.g. 'Daily backup job'.")),
			mcp.WithString("slug", mcp.Description("Optional stable ID; when a monitor with this slug exists it is updated (upsert). "+
				"Trimmed and lowercased, then it has to match ^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$ (3-50 chars, lowercase alphanumeric and hyphens). "+
				"UUID-shaped slugs are rejected (ambiguous with a monitor id in a Terraform import). Omitted: no slug.")),
			mcp.WithString("monitor_type", mcp.Description("'heartbeat' (default), 'ci' or 'http'; any other value is refused with 400 UNKNOWN_MONITOR_TYPE. "+
				"'ci' is a label, not a binding: without ci_provider it creates an ordinary heartbeat, and ci_workflow/ci_branch are refused.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple' (with period_s), 'cron' (with cron_expr) or 'on_demand' (neither). Required for heartbeat and ci monitors. "+
				"Not accepted on an http monitor, and neither are period_s, cron_expr and tz: its schedule comes from probe_interval_s (400 FIELD_NOT_IN_SHAPE). "+
				onDemandTradeoffDesc)),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds. Required when schedule_kind='simple'.")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression, e.g. '0 3 * * *'. Required when schedule_kind='cron'.")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation. Defaults to UTC.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds after a ping is due before alerting. "+onDemandGraceDefaultDesc+
				" On an upsert (existing slug), omitting it on an on_demand monitor sets 300.")),
			mcp.WithNumber("failure_threshold", mcp.Description(failureThresholdDesc+
				" On an upsert (existing slug), omitting it resets the threshold to 1.")),
			mcp.WithNumber("max_runtime_s", mcp.Description(maxRuntimeDesc+
				" On an upsert (existing slug), omitting it clears the value.")),
			mcp.WithNumber("step_timeout_s", mcp.Description(stepTimeoutDesc+
				" On an upsert (existing slug), omitting it clears the value and turns stall detection off.")),
			mcp.WithNumber("blocked_timeout_s", mcp.Description(blockedTimeoutDesc+
				" On an upsert (existing slug), omitting it clears the value back to the 24h default.")),
			mcp.WithNumber("expect_every_s", mcp.Description(expectEveryDesc+
				" On an upsert (existing slug), omitting it clears the value and turns the silence floor off.")),
			mcp.WithNumber("notify_min_run_s", mcp.Description(notifyMinRunDesc+
				" On an upsert (existing slug), omitting it clears the value and turns the floor off.")),
			mcp.WithNumber("runaway_ceiling", mcp.Description(runawayCeilingDesc+
				" On an upsert (existing slug), omitting it clears the ceiling and turns the runaway rule off.")),
			mcp.WithString("monitor_from", mcp.Description(monitorFromDesc+
				" On an upsert (existing slug), omitting it clears the value and arms the monitor immediately.")),
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
				" Omitted on a create: no owning agent. On an upsert (existing slug), omitting it leaves the current attachment unchanged, "+
				"and supplying it re-applies the attachment, so a repeated registration converges to 'attached'.")),
			mcp.WithString("trace_content", mcp.Enum("dropped", "redacted"), mcp.Description(traceContentDesc+
				" Omitted on a create: dropped; on an upsert (existing slug), omitting it leaves the stored value unchanged.")),
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
		newTool("list_monitors",
			mcp.WithDescription("Lists every monitor in the project with id, name, slug, status and ping_url. tag narrows the list to monitors carrying one tag."),
			mcp.WithString("tag", mcp.Description("Optional tag to filter by, e.g. 'agent:claude'. Only monitors with this tag are returned.")),
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
		newTool("get_monitor",
			mcp.WithDescription("Gets one monitor by UUID with its full configuration, including `assertions` (conditions a successful run's ping body has to satisfy), "+
				"`guards` (ceilings on a number the job reports) and `routes` (which destinations receive which event type); each is absent when empty. "+
				"update_monitor's assertions and guards and set_route each replace a whole set, and this result holds the current sets."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
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
			return c.getMonitor(ctx, id)
		},
	)

	// update_monitor
	s.AddTool(
		newTool("update_monitor",
			mcp.WithDescription("Updates a monitor by UUID with merge-patch semantics: supplied fields change, omitted fields keep their stored value. "+
				"tags, assertions (conditions a successful run's ping body has to satisfy, which catch a job that exits 0 having done nothing) and "+
				"guards (ceilings on a number the job reports, which catch a looping agent) each replace the whole set; get_monitor returns the current sets. "+
				"slug and ci_provider are immutable; only the ci_workflow and ci_branch filters change here."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable monitor name.")),
			mcp.WithString("schedule_kind", mcp.Description("'simple', 'cron' or 'on_demand'. Not accepted on an http monitor, and neither are period_s, "+
				"cron_expr and tz: its schedule comes from probe_interval_s (400 FIELD_NOT_IN_SHAPE). "+onDemandTradeoffDesc)),
			mcp.WithNumber("period_s", mcp.Description("Ping interval in seconds (for schedule_kind='simple').")),
			mcp.WithString("cron_expr", mcp.Description("5-field cron expression (for schedule_kind='cron').")),
			mcp.WithString("tz", mcp.Description("IANA timezone for cron evaluation.")),
			mcp.WithNumber("grace_s", mcp.Description("Grace period in seconds.")),
			mcp.WithNumber("failure_threshold", mcp.Description(failureThresholdDesc+" Omitted: unchanged.")),
			mcp.WithNumber("max_runtime_s", mcp.Description(maxRuntimeDesc+
				" Omitted: unchanged; 0 clears it (falls back to grace_s).")),
			mcp.WithNumber("step_timeout_s", mcp.Description(stepTimeoutDesc+
				" Omitted: unchanged; 0 clears it and turns stall detection off.")),
			mcp.WithNumber("blocked_timeout_s", mcp.Description(blockedTimeoutDesc+
				" Omitted: unchanged; 0 clears it (the 24h default applies).")),
			mcp.WithNumber("expect_every_s", mcp.Description(expectEveryDesc+
				" Omitted: unchanged; 0 clears it and turns the silence floor off.")),
			mcp.WithNumber("notify_min_run_s", mcp.Description(notifyMinRunDesc+
				" Omitted: unchanged; 0 clears it and turns the floor off.")),
			mcp.WithNumber("runaway_ceiling", mcp.Description(runawayCeilingDesc+
				" Omitted: unchanged; 0 clears it and turns the runaway rule off.")),
			mcp.WithString("monitor_from", mcp.Description(monitorFromDesc+
				" Omitted: unchanged.")),
			mcp.WithString("probe_url", mcp.Description(probeURLDesc+" Omitted: unchanged.")),
			mcp.WithNumber("probe_interval_s", mcp.Description(probeIntervalDesc+" Omitted: unchanged.")),
			mcp.WithString("probe_method", mcp.Description(probeMethodDesc+" Omitted: unchanged.")),
			mcp.WithNumber("probe_expected_status", mcp.Description(probeExpectedStatusDesc+" Omitted: unchanged.")),
			mcp.WithString("probe_expected_body", mcp.Description(probeExpectedBodyDesc+
				" Omitted or an empty string: unchanged; an explicit JSON null stops inspecting the body.")),
			mcp.WithNumber("probe_timeout_s", mcp.Description(probeTimeoutDesc+" Omitted: unchanged.")),
			mcp.WithBoolean("probe_follow_redirects", mcp.Description(probeFollowRedirectsDesc+" Omitted: unchanged; false turns following back off.")),
			mcp.WithString("ci_workflow", mcp.Description(ciWorkflowDesc+
				" Omitted or an empty string (an API compatibility rule): unchanged; an explicit JSON null removes the filter.")),
			mcp.WithString("ci_branch", mcp.Description(ciBranchDesc+
				" Omitted or an empty string (an API compatibility rule): unchanged; an explicit JSON null removes the filter.")),
			mcp.WithString("tags", mcp.Description("Comma-separated labels to set on this monitor, e.g. 'agent:claude,env:prod'. Replaces existing tags. Max 20 tags, each max 50 chars.")),
			mcp.WithString("agent_id", mcp.Description(agentIDDesc+" Omitted: the current attachment (or lack of one) is unchanged.")),
			mcp.WithString("assertions", mcp.Description(assertionsDesc)),
			mcp.WithString("guards", mcp.Description(guardsDesc)),
			mcp.WithString("trace_content", mcp.Enum("dropped", "redacted"), mcp.Description(traceContentDesc+
				" Omitted: unchanged.")),
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
		newTool("delete_monitor",
			mcp.WithDescription("Permanently deletes a monitor by UUID; this cannot be undone. "+
				"Its pings, run steps, traces, incidents, routes, alert templates, assertions and delivery history are deleted with it, "+
				"and the ingest keys bound to it (from create_ingest_key) are deleted too, so they stop working."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
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
			return c.deleteMonitor(ctx, id)
		},
	)

	// pause_monitor
	s.AddTool(
		newTool("pause_monitor",
			mcp.WithDescription("Pauses a monitor (paused=true): it still receives pings but raises no alert."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
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
			return c.simpleCheckPost(ctx, id, "pause")
		},
	)

	// resume_monitor
	s.AddTool(
		newTool("resume_monitor",
			mcp.WithDescription("Resumes a paused monitor (paused=false); alerting resumes from the next missed ping."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
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
			return c.simpleCheckPost(ctx, id, "resume")
		},
	)

	// snooze_monitor
	s.AddTool(
		newTool("snooze_monitor",
			mcp.WithDescription(snoozeMonitorDesc),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("duration", mcp.Description("Go duration string, e.g. '1h' or '24h'. One of duration, until or clear.")),
			mcp.WithString("until", mcp.Description("RFC 3339 end timestamp. One of duration, until or clear.")),
			mcp.WithBoolean("clear", mcp.Description("true removes the active maintenance window.")),
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
	} else if body["schedule_kind"] == "on_demand" {
		// Production, 2026-09-20: create_monitor with schedule_kind
		// on_demand and no grace_s failed 400 "check: grace 0s outside
		// [60, 31536000]", although grace_s is optional in this tool. An
		// on_demand monitor has no cadence, so its grace only sets the
		// first-run deadline and the overrun fallback, and asking an agent
		// to pick a number it cannot reason about is how the tool got
		// stuck. Scoped to on_demand, where the failure is: every other
		// create keeps the API's own rule.
		body["grace_s"] = onDemandDefaultGraceS
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
	if v, ok := args["trace_content"].(string); ok && v != "" {
		body["trace_content"] = v
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
			"If you lose it, no MCP tool can recover it: the secret must be regenerated from the dashboard, or through "+
			"POST /api/v1/checks/{id}/ci/regenerate with an ADMIN-scoped API key (a write-scoped key is refused), "+
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
	if v, ok := args["trace_content"].(string); ok && v != "" {
		body["trace_content"] = v
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
	// The description says "exactly one"; the API would silently pick one by
	// precedence (clear, then until, then duration), so more than one is
	// refused here rather than sent.
	if len(body) > 1 {
		named := make([]string, 0, len(body))
		for _, k := range []string{"duration", "until", "clear"} {
			if _, ok := body[k]; ok {
				named = append(named, k)
			}
		}
		return mcp.NewToolResultError("Provide exactly one of: duration, until, or clear=true; this call set " +
			strings.Join(named, " and ") + "."), nil
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

	// Decoded locally rather than through Check, so the snooze answer can
	// name the window's end without adding maintenance_until to every other
	// tool's monitor payload.
	var ch struct {
		Status           string  `json:"status"`
		MaintenanceUntil *string `json:"maintenance_until"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	if body["clear"] == true {
		return mcp.NewToolResultText(fmt.Sprintf("Maintenance window cleared on monitor %s (status=%s).", id, ch.Status)), nil
	}
	if ch.MaintenanceUntil != nil && *ch.MaintenanceUntil != "" {
		return mcp.NewToolResultText(fmt.Sprintf("Maintenance window set on monitor %s until %s (status=%s).", id, *ch.MaintenanceUntil, ch.Status)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Maintenance window set on monitor %s (status=%s).", id, ch.Status)), nil
}

// snoozeMonitorDesc says exactly what a maintenance window holds and what
// still notifies. Deadline incidents are held while a monitor is snoozed, and
// on an HTTP monitor every fail is treated as a probe's and held too; the
// job's own fail ping elsewhere, the page a blocked ping queues, a runaway
// ping rate and every routed info event still notify. Matches the hosted
// server at mcp.lastping.dev verbatim.
const snoozeMonitorDesc = "Sets or clears a maintenance window on a monitor. During it, deadline incidents (a missed or never-started run, an overrun, a stall, " +
	"a blocked run outliving blocked_timeout_s) and, on an HTTP monitor, failing probes (any fail there counts as a probe's) are recorded but open no incident; " +
	"a site still failing afterwards opens one on its next failure. " +
	"Still notified: the job's own fail ping, the page a blocked ping queues, a runaway ping rate, and routed note, started, success and every-run events. " +
	"Takes exactly one of duration, until or clear=true."
