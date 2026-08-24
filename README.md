<div align="center">

<img src=".github/banner.svg" alt="LastPing" width="100%">


[![MIT](https://img.shields.io/badge/license-MIT-0f766e)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-0f766e)](go.mod)
[![Terraform](https://img.shields.io/badge/terraform-lastping--dev%2Flastping-0f766e)](https://registry.terraform.io/providers/lastping-dev/lastping/latest)
[![Free](https://img.shields.io/badge/free_for_individuals-0f766e)](https://lastping.dev)

</div>

---

Most monitoring watches a thing and tells you when it looks wrong. LastPing
waits for a thing to check in and tells you when it doesn't. That inversion is
the whole product: **a job that breaks can't send you an error, but it can fail
to send you anything** — and absence is the one signal a broken process can
still produce.

This repository holds the open-source pieces: the `lastping` CLI and the MCP
server. The hosted service they talk to is at **[lastping.dev](https://lastping.dev)**,
free for individuals.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/tp322d/lastping-app/main/install.sh | sh
```

No Go toolchain needed — that pulls a prebuilt binary for macOS and Linux, on
amd64 and arm64, and verifies its checksum. Windows builds are on the
[releases page](https://github.com/tp322d/lastping-app/releases).

If you do have Go:

```sh
go install github.com/tp322d/lastping-app/cmd/lastping@latest
```

## `lastping run` — reporting you can't forget

Put it in front of whatever you already run:

```sh
lastping run --monitor <monitor-id> -- python nightly_etl.py
lastping run --monitor <monitor-id> -- ./backup.sh
lastping run --monitor <monitor-id> -- claude
```

It sends a start ping, runs your command untouched, and reports the exit code
when it finishes — success on 0, failure on anything else, with the tail of
stderr attached so the alert says *why*.

Three properties worth knowing, because they are the difference between a
monitoring wrapper you can trust in production and one you remove after a bad
night:

- **Your exit code always propagates.** The wrapper exits with whatever your
  command exited with, so CI behaves exactly as it did before you added it.
- **A failed ping never touches your command.** If LastPing is unreachable, your
  job still runs, still writes its output, still exits normally.
- **Interactive stays interactive.** stdin and stdout are handed over as file
  descriptors, so wrapping a REPL or an agent session works.

Why a wrapper rather than an instruction? Because anything advisory decays. An
AI agent told to report on every task will stop doing it, and a cron line you
meant to add a `curl` to never gets it. A wrapper reports from the process
lifecycle, so nothing depends on anybody remembering.

## MCP server — let an agent set up its own monitoring

```jsonc
// claude_desktop_config.json, .mcp.json, or your client's equivalent
{
  "mcpServers": {
    "lastping": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://mcp.lastping.dev/mcp",
               "--header", "Authorization: Bearer ${LASTPING_API_KEY}"],
      "env": { "LASTPING_API_KEY": "lp__your_key_here" }
    }
  }
}
```

The hosted server is the recommended path — nothing to install, and it always
carries the current tool set.

A stdio binary is also here if you would rather run it yourself:

```sh
go install github.com/tp322d/lastping-app/cmd/lastping-mcp@latest
```

<details>
<summary><b>Tools in this repository's stdio binary (35)</b></summary>

Monitors: `create_monitor` · `get_monitor` · `list_monitors` ·
`update_monitor` · `delete_monitor` · `pause_monitor` · `resume_monitor` ·
`snooze_monitor`

Reporting: `get_ping_instructions` · `declare_run_expectations`

Incidents & runs: `list_incidents` · `get_run_history`

The failure loop: `list_open_incidents` · `add_incident_note`

Alert routing: `set_route`

Destinations: `list_destinations` · `create_destination` ·
`update_destination` · `test_destination` · `delete_destination`

Alert templates: `get_alert_templates` · `set_alert_template`

Agent registry: `register_agent` · `list_agents` · `get_agent` ·
`update_agent` · `delete_agent`

Status pages: `list_status_pages` · `create_status_page` ·
`update_status_page` · `delete_status_page`

API keys: `create_api_key` · `list_api_keys` · `revoke_api_key`

Terraform: `export_terraform`

This binary carries the same tool set as the hosted server at
`mcp.lastping.dev`. It is a thin REST client throughout: every tool is a
direct HTTP call to the management API, so it stays free to run yourself with
no lag behind the hosted surface beyond a new release.

</details>

The one that matters most is `get_ping_instructions`: an agent calls
`create_monitor`, then asks for its own ping commands, and wires them into its
own work — in one conversation, without a human opening a dashboard.

## Ping API

Every monitor gets a URL. There is nothing to install and no library to keep
current; anything that can make an HTTP request can report.

| What happened | Request |
|---|---|
| finished successfully | `POST <ping-url>` |
| started a run | `POST <ping-url>/start` |
| failed | `POST <ping-url>/fail` with the error as the body |
| exited with a code | `POST <ping-url>/<exit-code>` |
| waiting on a human | `POST <ping-url>/blocked` |
| progress worth recording | `POST <ping-url>/note` |

Add `?rid=<id>` to pair a run's start with its result, so LastPing can group a
run's pings and time it.

```sh
# The classic one-liner, at the end of a cron job:
curl -fsS -m 10 --retry 3 https://ping.lastping.dev/<monitor-id>
```

## Monitoring as code

```hcl
resource "lastping_monitor" "nightly_etl" {
  name          = "nightly-etl"
  slug          = "nightly-etl"
  schedule_kind = "cron"
  cron_expr     = "0 3 * * *"
  tz            = "Europe/Berlin"
  grace_s       = 900
}
```

The provider is on the
[Terraform Registry](https://registry.terraform.io/providers/lastping-dev/lastping/latest)
as `lastping-dev/lastping`, with source at
[lastping-dev/terraform-provider-lastping](https://github.com/lastping-dev/terraform-provider-lastping).

## Links

- **[lastping.dev](https://lastping.dev)** — the hosted service, free for individuals
- **[AI agent monitoring](https://lastping.dev/agents/)** — the agent-first guide
- **[MCP server](https://lastping.dev/mcp/)** — connect configs per client
- **[Terraform provider](https://lastping.dev/terraform)** — monitoring as code
- **[Integration guides](https://lastping.dev/monitor/)** — cron, Kubernetes, systemd, GitHub Actions, Python, Node

## License

MIT. See [LICENSE](LICENSE).
