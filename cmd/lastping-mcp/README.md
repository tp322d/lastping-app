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

## Tools

**Monitors:** `create_monitor` (upsert by slug) · `list_monitors` · `get_monitor` · `update_monitor` · `delete_monitor` · `pause_monitor` · `resume_monitor` · `snooze_monitor`
**Incidents:** `list_incidents` · `get_incident`
**Destinations:** `list_destinations`
**Self-instrumentation:** **`get_ping_instructions`** — returns a monitor's ping URL plus ready-to-run success / start / fail snippets, so the agent can make the monitored job actually check in.

## The flow that makes agents self-monitoring

1. `create_monitor` → e.g. a heartbeat expected every day, or a cron-scheduled agent run
2. `get_ping_instructions` → get `curl_success`, `curl_start`, `curl_fail` for that monitor
3. Run the **success** ping at the end of the job; the **fail** ping if it errors; the **start** ping first for long/hung runs (overrun + never-finished detection)

If the run goes silent, LastPing opens an incident and alerts you — without the agent having to notice it died.
