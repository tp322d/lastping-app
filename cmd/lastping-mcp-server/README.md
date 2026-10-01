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

`GET /healthz` → 200 (for a load balancer or orchestrator health check). The
MCP endpoint is at `/`, behind bearer auth (missing/invalid → 401) and a
per-token rate limit (429).

## Run it yourself

The same server runs hosted at `mcp.lastping.dev`; you only need to run it if
you want your own endpoint. Build the container image from the repository root:

```sh
docker build -f cmd/lastping-mcp-server/Dockerfile -t lastping-mcp-server .
docker run -p 8080:8080 lastping-mcp-server
```

or build the binary directly with
`go build ./cmd/lastping-mcp-server`. Point `LP_API_BASE` at a different API
only if you run one; by default it proxies to `https://app.lastping.dev`.

The image is distroless, so it has no shell or curl for a health probe:
`lastping-mcp-server -healthcheck` dials the local `/healthz` and exits 0 or 1,
which is what a container health check should run.
