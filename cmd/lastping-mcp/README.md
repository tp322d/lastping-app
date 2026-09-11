# lastping-mcp

An [MCP](https://modelcontextprotocol.io) server for **LastPing** — the dead-man's-switch monitor for cron jobs, backups, CI/CD pipelines, and **AI agents**.

It lets an AI agent (Claude Desktop / Claude Code, Cursor, etc.) create and manage LastPing monitors, query incidents, and — the flagship — **instrument its own dead-man's-switch in a single conversation**: create a monitor, get the ping snippet, and wire it into the job it runs.

## Hosted (recommended)

You don't have to build or run anything — LastPing hosts the MCP server at
**`https://mcp.lastping.dev`** (Streamable HTTP). Point your client at it with
your API key:

```jsonc
{ "mcpServers": { "lastping": {
    "url": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

Get a key at app.lastping.dev → Settings → API keys. The rest of this README
covers **self-hosting** the stdio binary if you'd rather run it locally.

## Self-host (stdio binary)

A single Go binary. No npm, no runtime dependencies. The public source lives at
`github.com/tp322d/lastping-app`:

```bash
go install github.com/tp322d/lastping-app/cmd/lastping-mcp@latest
```

## Build from source

```bash
go build -o lastping-mcp ./cmd/lastping-mcp
```

## Configure

| Env var | Required | Default |
|---|---|---|
| `LASTPING_API_KEY` | **yes** | — (get one at app.lastping.dev → Settings → API Keys) |
| `LASTPING_BASE_URL` | no | `https://app.lastping.dev` |
| `LASTPING_PING_HOST` | no | `https://ping.lastping.dev` |

## Add to your MCP client

Claude Desktop (`claude_desktop_config.json`), Cursor, or a project `.mcp.json`:

```json
{
  "mcpServers": {
    "lastping": {
      "command": "/absolute/path/to/lastping-mcp",
      "env": { "LASTPING_API_KEY": "lp_your_key_here" }
    }
  }
}
```

## Tools (36)

**Monitors:** `create_monitor` (upsert by slug) · `list_monitors` · `get_monitor` · `update_monitor` · `delete_monitor` · `pause_monitor` · `resume_monitor` · `snooze_monitor`
**Discovery:** `discover_monitors_reconcile` — turns a scan of a repository or a host into monitors: send every scheduled job you found, get back a three-way diff of what was created, what already existed, and what has gone missing. Scanning is agent-side; LastPing never reads your repository. Propose what you found to the user before calling — it *creates* monitors. It never deletes, pauses or edits anything, which is what makes it safe to re-run on a schedule as drift detection rather than a one-off setup step.
**Incidents & runs:** `list_incidents` · `get_run_history`
**The failure loop:** `list_open_incidents` — the agent's inbox: every incident currently open on the monitors it owns, newest first, carrying facts a single run cannot know on its own (`failure_signature.occurrences`, the failing step, the exit code, duration vs. normal). A missing enrichment means *no evidence*, never "normal" and never "exited cleanly". · `add_incident_note` — writes the agent's diagnosis back onto the incident a human reads, whether or not it could fix the problem. Append-only: a correction is a new note, never an edit.
**Alert routing:** `set_route`
**Destinations:** `list_destinations` · `create_destination` · `update_destination` · `test_destination` · `delete_destination`
**Alert templates:** `get_alert_templates` · `set_alert_template`
**Agent registry:** `register_agent` · `list_agents` · `get_agent` · `update_agent` · `delete_agent`
**Status pages:** `list_status_pages` · `create_status_page` · `update_status_page` · `delete_status_page`
**API keys:** `create_api_key` · `list_api_keys` · `revoke_api_key`
**Terraform:** `export_terraform`
**Self-instrumentation:** **`get_ping_instructions`** — returns a monitor's ping URL plus ready-to-run success / start / fail snippets, so the agent can make the monitored job actually check in. · `declare_run_expectations` — commit, at the *start* of a run, to the criteria that run will be judged by, before the outcome is knowable. Immutable once declared.

### API key scopes

Every tool description starts with the scope its API key needs: `read` (the
twelve `list_*` / `get_*` tools and `export_terraform`), `write` (everything
that changes LastPing state, plus `test_destination`, which sends a real
message to a third party), or `admin` (the three key-management tools —
`list_api_keys` included, because key names, prefixes, expiries and lineage are
what you need to choose which key to revoke).

The scope is stated up front so an agent can anticipate a refusal instead of
discovering it from a 403 and retrying. Nothing is enforced here: this binary
is a REST client and the hosted API is the only authorization layer. A refusal
names the tier it wanted — a 403 carries `required_scope`, and a
`create_api_key` call asking for more than the creating key holds comes back
with `max_scope` — and both are folded into the tool's error text.

`create_api_key` takes an optional `scope` (`read`, `write` or `admin`;
defaults to `write` server-side). A key can never be given a higher scope, or a
longer life, than the key that creates it. `revoke_api_key` cascades: it
revokes the named key **and every key it created**, recursively, and reports
how many went.

### Destination host rules

`create_destination` and `update_destination` state the host rule the API
applies, because a bare 400 gives an agent nothing to correct. Every
destination URL must be https. Branded kinds are pinned to their vendor's
hosts: `slack` to `hooks.slack.com`, `discord` to `discord.com` or
`discordapp.com`, `msteams` to `webhook.office.com`, `outlook.office.com`,
`logic.azure.com`, `logic.azure.us` or `environment.api.powerplatform.com`, and
`googlechat` to `chat.googleapis.com`. `webhook` accepts any https host and
`ntfy` is unpinned, so a self-hosted ntfy server is fine.

A pin narrows a destination to the vendor's own platform. It does **not** prove
the endpoint belongs to whoever created it — every pinned domain is
multi-tenant and self-service — so a pinned destination is not "verified" or
"owned" on the strength of its host. A project holds at most 25 destinations;
`DESTINATION_CAP_REACHED` means delete one, not retry.

## The flow that makes agents self-monitoring

1. `create_monitor` → e.g. a heartbeat expected every day, or a cron-scheduled agent run
2. `get_ping_instructions` → get `curl_success`, `curl_start`, `curl_fail` for that monitor
3. Run the **success** ping at the end of the job; the **fail** ping if it errors; the **start** ping first for long/hung runs (overrun + never-finished detection)

If the run goes silent, LastPing opens an incident and alerts you — without the agent having to notice it died.
