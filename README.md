# LastPing

**The only free tool that monitors your cron jobs *and* your CI/CD pipelines in one place — and when a pipeline fails, it tells you why.**

[**lastping.dev**](https://lastping.dev) · [Dashboard](https://app.lastping.dev) · [Docs](https://app.lastping.dev/docs) · [Compare vs Healthchecks](https://lastping.dev/vs/healthchecks/)

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

## MCP server for AI agents (open source)

This repo is the home of **`lastping-mcp`** — the open-source [MCP](https://modelcontextprotocol.io) server that lets an AI agent (Claude, Cursor, …) create and manage LastPing monitors, query incidents, and **instrument its own dead-man's-switch in a single conversation**.

**Hosted (recommended)** — nothing to install; point your MCP client at the hosted server with your API key:

```jsonc
{ "mcpServers": { "lastping": {
    "url": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

**Self-host** the stdio binary instead:

```bash
go install github.com/tp322d/lastping-app/cmd/lastping-mcp@latest
```

See [`cmd/lastping-mcp/`](cmd/lastping-mcp/) (stdio, self-host), [`cmd/lastping-mcp-server/`](cmd/lastping-mcp-server/) (the hosted Streamable-HTTP server), and [`cmd/lastping-mcp/AGENTS.md`](cmd/lastping-mcp/AGENTS.md) for the agent-facing guide. Get an API key at [app.lastping.dev](https://app.lastping.dev) → Settings → API keys. MIT-licensed.

## Free & hosted

LastPing is **free and fully hosted** today — just sign up at [lastping.dev](https://lastping.dev). This repository is the project's public home (announcements, docs links, issues); the application is operated as a hosted service.

## Links

- Site: https://lastping.dev
- App: https://app.lastping.dev
- Docs / API reference: https://app.lastping.dev/docs
- Monitor GitHub Actions: https://lastping.dev/monitor/github-actions/
- vs Healthchecks: https://lastping.dev/vs/healthchecks/ · vs Cronitor: https://lastping.dev/vs/cronitor/

<!-- TODO(design): add dashboard + CI-failure-detail screenshots here (task #49) -->
