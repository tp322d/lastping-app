# LastPing

**The only free tool that monitors your cron jobs *and* your CI/CD pipelines in one place — and when a pipeline fails, it tells you why.**

[**lastping.dev**](https://lastping.dev) · [Dashboard](https://app.lastping.dev) · [Docs](https://app.lastping.dev/docs) · [MCP server](https://lastping.dev/mcp/) · [Compare vs Healthchecks](https://lastping.dev/vs/healthchecks/)

Cron jobs, backups, and CI/CD pipelines fail silently — the job that breaks can't tell you it broke. LastPing waits for each one to check in and opens an incident the moment it doesn't. It's **free and fully hosted** — no credit card, no paid tier.

---

## Why LastPing

- **Crons *and* pipelines, one console.** Heartbeat monitors for cron jobs, backups, and any scheduled task — alongside first-class CI/CD monitoring. Most free tools do one or the other.
- **It tells you *why* CI failed.** Connect a GitHub Actions workflow with a signed `workflow_run` webhook and LastPing catches failed, hung, and never-started runs — and can attach the failing log excerpt to the alert, so the incident isn't just "something broke."
- **Alerts on the very first check.** Email works out of the box (no routing to configure). Add Telegram, Slack, Discord, or signed outbound webhooks, and route which events reach which channel per monitor.
- **Free, with no asterisk.** No monitor caps hidden behind a paid tier.
- **We monitor ourselves with it.** LastPing watches its own infrastructure — see the live [self-monitoring status page](https://app.lastping.dev/status/lastping-self).

## Quickstart

Create a monitor in the [dashboard](https://app.lastping.dev), then have your job ping its URL when it finishes:

```bash
# At the end of your cron job / backup script:
curl -fsS -m 10 --retry 3 https://ping.lastping.dev/your-monitor-uuid
```

If that ping doesn't arrive within the schedule + grace window you set, LastPing opens an incident and alerts you. Signal the *start* of a long job (to catch overruns) or a hard *failure* with `/start` and `/fail` suffixes.

For GitHub Actions, point a `workflow_run` webhook at your CI monitor and LastPing tracks every run — see the [GitHub Actions guide](https://lastping.dev/monitor/github-actions/).

## What it does

- Heartbeat / dead-man's-switch monitoring (interval or cron schedules, timezone-aware, configurable grace)
- HTTP / uptime probing
- CI/CD pipeline monitoring via signed GitHub `workflow_run` webhooks, with failure-detail capture
- Incidents with a full event timeline
- Alert channels: email, Telegram, Slack, Discord, outbound webhooks — with per-monitor event routing and custom message templates
- Public status pages and status badges
- A full REST API (interactive [API docs](https://app.lastping.dev/docs)) — everything you can do in the UI, you can do via the API

---

## MCP server for AI agents (open source)

This repo is the home of **`lastping-mcp`** — the open-source [MCP](https://modelcontextprotocol.io) server that lets an AI agent (Claude, Cursor, Windsurf, …) create and manage LastPing monitors, query incidents, and **instrument its own dead-man's-switch in a single conversation**.

Full documentation: [lastping.dev/mcp/](https://lastping.dev/mcp/)

### What an agent can do

Once the MCP server is connected, an agent can:

- **Create monitors** — heartbeat (cron or interval), CI/CD (pipeline signals), or HTTP/uptime — and upsert them by slug so re-runs are idempotent
- **Instrument itself** — call `get_ping_instructions` to receive ready-to-run `curl_success`, `curl_start`, and `curl_fail` snippets; wire them into its own workflow; from that point on any silence, hang, or failure opens a LastPing incident automatically
- **Manage its monitor lifecycle** — pause during maintenance windows (`snooze_monitor`), check status (`get_monitor`), and inspect incident history (`list_incidents`) without leaving the conversation
- **Set up alert routing** — create a Slack, Telegram, email, or webhook destination (`create_destination`), verify it fires (`test_destination`), then route `down`/`recovery`/`fail` events to it (`set_route`)
- **List everything** — `list_monitors`, `list_destinations`, `list_incidents` give the agent a full picture of what exists before making changes

The key insight: competitors' MCP servers are positioned for *reading* pre-existing monitoring data. LastPing MCP is positioned for an agent *creating and owning* its own monitor — the agent sets up its watchdog, not a human.

### The self-monitoring narrative

An autonomous agent is the new cron job. It runs unattended, on a schedule or in a loop, and its worst failures are silent: the process is alive, the work has stopped. LastPing is the first monitor your agent can set up itself, in one conversation, without opening a dashboard:

1. **`create_monitor`** — register a heartbeat for the task. Pass a stable `slug` so the operation is idempotent (upsert, not duplicate).
2. **`get_ping_instructions`** — receive `curl_success`, `curl_start`, and `curl_fail` snippets for that monitor.
3. **Wire the pings** — success ping at the end of the work; fail ping on any error path; start ping at launch to arm overrun + never-finished detection.

If the agent goes silent — crash, hang, OOM, or a scheduled run that stops firing — LastPing opens an incident and alerts the operator. The agent's watchdog runs whether or not the agent is alive.

### The 14 MCP tools

**Monitors**

| Tool | What it does |
|---|---|
| `create_monitor` | Create or upsert a monitor by slug (heartbeat · ci · http). Idempotent — safe for agents to retry |
| `list_monitors` | List all monitors with id, name, slug, status, ping_url |
| `get_monitor` | Get a single monitor's full config by UUID |
| `update_monitor` | Update an existing monitor's schedule and config |
| `delete_monitor` | Permanently delete a monitor |
| `pause_monitor` | Pause alerting; monitor still receives pings but does not open incidents |
| `resume_monitor` | Resume a paused monitor; alerting resumes on the next missed ping |
| `snooze_monitor` | Set or clear a maintenance window (duration, RFC 3339 timestamp, or clear=true) |

**Incidents**

| Tool | What it does |
|---|---|
| `list_incidents` | List recent incidents for a monitor, newest first; open incidents have `closed_at=null` |

**Destinations**

| Tool | What it does |
|---|---|
| `list_destinations` | List all notification destinations (email, webhook, Slack, Discord, Telegram, ntfy, Pushover, Teams, Google Chat) |
| `create_destination` | Create a notification destination; email requires confirmation before routing |
| `test_destination` | Deliver a synthetic test alert to verify credentials |

**Routing**

| Tool | What it does |
|---|---|
| `set_route` | Route a monitor's `down` / `recovery` / `fail` alerts to a set of destinations (replaces the full set; empty clears routing) |

**Self-instrumentation**

| Tool | What it does |
|---|---|
| `get_ping_instructions` | **The flagship.** Returns the ping URL plus ready-to-run `curl_success`, `curl_start`, and `curl_fail` snippets. Call right after `create_monitor`. |

### Hosted (recommended)

Nothing to install. Point your MCP client at the hosted server with your API key:

**Claude Desktop** (`claude_desktop_config.json`):
```jsonc
{ "mcpServers": { "lastping": {
    "url": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

**Claude Code CLI** (one command, user scope):
```bash
claude mcp add --transport http --scope user lastping https://mcp.lastping.dev \
  --header "Authorization: Bearer lp_…"
```

**Cursor** (`~/.cursor/mcp.json` for user scope, or `.cursor/mcp.json` for project scope):
```jsonc
{ "mcpServers": { "lastping": {
    "url": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

**Windsurf** (`~/.codeium/windsurf/mcp_config.json` — Windsurf uses `serverUrl`):
```jsonc
{ "mcpServers": { "lastping": {
    "serverUrl": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

Get an API key at [app.lastping.dev](https://app.lastping.dev) → Settings → API keys.

### Self-host (stdio binary)

A single Go binary. No npm, no runtime dependencies. MIT-licensed.

```bash
# requires Go 1.22+
go install github.com/tp322d/lastping-app/cmd/lastping-mcp@latest
```

Then configure with `command` + `env` instead of `url` + `headers`:

```jsonc
{ "mcpServers": { "lastping": {
    "command": "lastping-mcp",
    "env": { "LASTPING_API_KEY": "lp_your_key_here" } } } }
```

Optional env vars: `LASTPING_BASE_URL` (default: `https://app.lastping.dev`) and `LASTPING_PING_HOST` (default: `https://ping.lastping.dev`) — useful for self-hosted LastPing instances.

See [`cmd/lastping-mcp/`](cmd/lastping-mcp/) (stdio binary), [`cmd/lastping-mcp-server/`](cmd/lastping-mcp-server/) (the hosted Streamable-HTTP server at `mcp.lastping.dev`), and [`cmd/lastping-mcp/AGENTS.md`](cmd/lastping-mcp/AGENTS.md) for the ping conventions and self-instrumentation flow.

### Example prompts

- *"Monitor my nightly Postgres backup and alert me on Telegram if it doesn't check in by 3am."*
- *"Add a LastPing dead-man's-switch to the deploy pipeline you just wrote — daily at midnight, 30-minute grace."*
- *"Create a monitor for this nightly summary agent — runs at 2am UTC, alert me on Slack if it goes silent."*
- *"Check all my monitors. Which ones have had incidents in the past week?"*
- *"Snooze the nightly-backup monitor for 4 hours while I run a maintenance window."*
- *"Instrument yourself: register a heartbeat for this agent at '0 6 * * *' (6am daily, 45-minute grace), get the ping snippets, and add them to your run loop."*

### Ping conventions

Once a monitor exists, pinging it is plain HTTP — no SDK, no import:

- `GET https://ping.lastping.dev/<uuid>` — success / "I'm alive"
- `.../start` — run began (enables overrun + never-finished detection)
- `.../fail` — run failed (POST error/log body as `text/plain`, up to 64 KB — stored on the incident)
- `.../<exit-code>` — 0 = success, non-zero = fail
- `...?rid=<run-id>` — pair a start with its later success/fail and record run duration

---

## Free & hosted

LastPing is **free and fully hosted** today — just sign up at [lastping.dev](https://lastping.dev). This repository is the project's public home (announcements, docs links, issues); the application is operated as a hosted service.

## Links

- Site: https://lastping.dev
- App: https://app.lastping.dev
- MCP server: https://lastping.dev/mcp/
- Docs / API reference: https://app.lastping.dev/docs
- Monitor GitHub Actions: https://lastping.dev/monitor/github-actions/
- vs Healthchecks: https://lastping.dev/vs/healthchecks/ · vs Cronitor: https://lastping.dev/vs/cronitor/

<!-- TODO(design): add dashboard + CI-failure-detail screenshots here (task #49) -->
