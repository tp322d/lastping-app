# lastping-mcp-server

The **remote** MCP server for LastPing — serves the same tools as the stdio
`lastping-mcp` binary over MCP **Streamable HTTP**, hosted at
**`https://mcp.lastping.dev`**. Agents connect with a URL + API key; nothing to
install.

It is a thin transport + auth layer: each request's `Authorization: Bearer
<api-key>` becomes a per-request client, and the tool handlers (shared from
`internal/mcptools`) proxy to the LastPing API. No business logic lives here.

## Connect a client

```jsonc
{ "mcpServers": { "lastping": {
    "url": "https://mcp.lastping.dev",
    "headers": { "Authorization": "Bearer lp_your_key_here" } } } }
```

Claude Code: `claude mcp add --transport http --scope user lastping https://mcp.lastping.dev --header "Authorization: Bearer lp_…"`.

## Config (env)

| Env var | Default | Notes |
|---|---|---|
| `LP_API_BASE` | `https://app.lastping.dev` | LastPing API the tools proxy to |
| `LP_MCP_PORT` | `8080` | listen port |
| `LP_PING_HOST` | `https://ping.lastping.dev` | ping host for `get_ping_instructions` |

`GET /healthz` → 200 (ALB health check). The MCP endpoint is at `/`, behind
bearer auth (missing/invalid → 401) and a per-token rate limit (429).

## Deploy

Built + shipped by the normal pipeline: `deploy.yml` builds
`cmd/lastping-mcp-server/Dockerfile` into the `lastping-prod-mcp` ECR repo, and
`infra/terraform/mcp.tf` runs it as a dedicated Fargate service behind the ALB
(host `mcp.lastping.dev`). `run -healthcheck` is the container health probe.

## Local / e2e

`docker compose up mcp` runs it against the in-network `api`; the protocol is
exercised end-to-end in `test/e2e/mcp_remote_test.go`.
