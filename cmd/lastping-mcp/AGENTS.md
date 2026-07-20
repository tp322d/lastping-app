# Using LastPing from an AI agent

LastPing is a **dead-man's-switch monitor**: something checks in on a schedule, and if the check-in doesn't arrive, LastPing opens an incident and alerts a human. That makes it the natural way for an autonomous agent to prove it's still alive and doing its job — and to get someone notified when it isn't (crash, hang, infinite loop, or a scheduled run that silently stops firing).

## Via MCP (recommended)

Point your MCP client at the hosted server **`https://mcp.lastping.dev`** with an
`Authorization: Bearer <api-key>` header — nothing to install — or self-host the
`lastping-mcp` stdio binary (see `README.md`). Then, in a single conversation, an
agent can set up its own monitoring:

1. **`create_monitor`** — create a heartbeat (expected every N seconds, or on a cron) for the task you run. Pass a stable `slug` so re-running is idempotent (it upserts, not duplicates).
2. **`get_ping_instructions`** — get the ping URL + ready-to-run `curl_success` / `curl_start` / `curl_fail` snippets for that monitor.
3. **Wire the ping into your work** — run the **success** ping when the task finishes, the **fail** ping if it errors, and (for long or possibly-hung work) the **start** ping first.

You can also `list_incidents`, `snooze_monitor` during planned downtime, and `pause_monitor`/`resume_monitor`.

## Via the REST API directly

Everything the MCP server does maps to `POST`/`GET`/`PATCH` `/api/v1/...` with `Authorization: Bearer <api-key>`. Reference: <https://app.lastping.dev/docs/api/> and <https://lastping.dev/llms.txt>.

## Ping conventions

- `GET https://ping.lastping.dev/<monitor-uuid>` — success / "I'm alive"
- `.../start` — a run began (enables overrun + never-finished detection)
- `.../fail` — the run failed (you may POST the error/log body, stored as failure detail)
- `.../<exit-code>` — report a shell exit code (0 = success, non-zero = fail)
- `...?rid=<run-id>` — pair a start with its later success/fail and record run duration
