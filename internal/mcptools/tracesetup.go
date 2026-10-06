package mcptools

// tracesetup.go: get_trace_setup and create_ingest_key.
//
// Both are THIN REST PROXIES, exactly like get_ping_instructions:
// get_trace_setup over GET /api/v1/checks/{id}/trace-setup and
// create_ingest_key over POST /api/v1/checks/{id}/ingest-keys. Nothing is
// built here: the set-up blocks are assembled by the server, and this package
// decodes the route's JSON into the mirror structs below, so a change on the
// server reaches every installed client with no release. It matches the
// hosted server at mcp.lastping.dev.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TraceSetup mirrors GET /api/v1/checks/{id}/trace-setup's body. Field names
// and tags must stay byte-for-byte identical to the API's: an MCP client
// decodes this exact shape, and a proxy that decodes into a struct silently
// drops whatever the struct does not name.
type TraceSetup struct {
	Tool   string            `json:"tool"`
	Blocks []TraceSetupBlock `json:"blocks"`
	Prompt string            `json:"prompt"`
}

// TraceSetupBlock mirrors one entry of the route's blocks array.
type TraceSetupBlock struct {
	Tool        string           `json:"tool"`
	Title       string           `json:"title"`
	Writes      string           `json:"writes"`
	Command     string           `json:"command"`
	Steps       []string         `json:"steps"`
	Files       []TraceSetupFile `json:"files"`
	EnvLines    []string         `json:"env_lines"`
	Verify      string           `json:"verify"`
	ConsoleLink string           `json:"console_link"`
	Limits      string           `json:"limits"`
	SecretInURL bool             `json:"secret_in_url"`
	BangCommand string           `json:"bang_command"`
	// BangScript is null for a tool without a reviewed script; never
	// omitempty, so the agent gets exactly what the route served.
	BangScript *TraceSetupFile `json:"bang_script"`
	KeyLine    string          `json:"key_line"`
}

// TraceSetupFile mirrors one entry of a block's files array.
type TraceSetupFile struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Content  string `json:"content"`
	Mode     string `json:"mode"`
	Merge    bool   `json:"merge"`
}

// traceSetupTools is the tool names the ?tool= parameter accepts, in the
// server's order. The server validates it too; listing it here puts it in
// the tool's schema, where an agent reads it before calling.
var traceSetupTools = []string{"claude-code", "codex", "gemini", "antigravity", "cursor", "python", "node", "otel-sdk", "collector"}

func registerTraceSetupTools(s *server.MCPServer) {
	s.AddTool(
		newTool("get_trace_setup",
			mcp.WithDescription("Returns the steps that make a tool send OpenTelemetry traces to one monitor: what to write and where, how to verify it, "+
				"and the tool's limits. For setting up tracing, observability or telemetry. `prompt` is the full set-up procedure for the named tool, "+
				"credential handling included. Each block has `files`, a `key_line` (the terminal line that stores the tracing key the person creates "+
				"on the monitor's Connect page) and, for Claude Code and Codex, a reviewable `bang_script` run with `bang_command`."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
			mcp.WithString("tool",
				mcp.Enum(traceSetupTools...),
				mcp.Description("Which tool sends the traces: claude-code, codex, gemini, antigravity, cursor, python, node, otel-sdk or collector. Omitted: every block.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("monitor_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.getTraceSetup(ctx, id, req.GetString("tool", ""))
		},
	)

	s.AddTool(
		newTool("create_ingest_key",
			mcp.WithDescription("Creates a tracing key: an ingest-scoped key bound to one monitor, able to send traces, metrics, logs and pings for it "+
				"and nothing else; every REST call refuses it. For automation that stores the key itself (a CI secret, a deployment's secret store). "+
				"The plaintext key appears only in this result, which puts it in the conversation; the console's Create a tracing key keeps it out of chat."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor UUID the key is bound to (from create_monitor or list_monitors).")),
			mcp.WithString("name", mcp.Description("Optional label for the key. Defaults to \"Tracing:\" followed by the monitor's name.")),
			mcp.WithString("expires_at", mcp.Description("Optional RFC 3339 expiry, e.g. \"2026-12-31T00:00:00Z\". Omitted: 90 days. Either way it is capped at the "+
				"creating key's own expiry.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("monitor_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createIngestKey(ctx, id, req.GetString("name", ""), req.GetString("expires_at", ""))
		},
	)
}

// getTraceSetup proxies GET /api/v1/checks/{id}/trace-setup and re-renders
// the response as paste-ready JSON.
func (c *APIClient) getTraceSetup(ctx context.Context, id, tool string) (*mcp.CallToolResult, error) {
	target := c.BaseURL + "/api/v1/checks/" + url.PathEscape(id) + "/trace-setup"
	if tool != "" {
		target += "?tool=" + url.QueryEscape(tool)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs, or create_monitor first.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ts TraceSetup
	if err := json.NewDecoder(resp.Body).Decode(&ts); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(marshalNoEscape(ts)), nil
}

// createIngestKey proxies POST /api/v1/checks/{id}/ingest-keys through the
// same expiry rule as create_api_key (mintKey). The body is only ever name
// and expires_at: the route fixes the scope and the monitor.
func (c *APIClient) createIngestKey(ctx context.Context, id, name, expiresAt string) (*mcp.CallToolResult, error) {
	payload := map[string]any{}
	if name != "" {
		payload["name"] = name
	}
	created, errResult := c.mintKey(ctx, "/api/v1/checks/"+url.PathEscape(id)+"/ingest-keys", payload, expiresAt)
	if errResult != nil {
		return errResult, nil
	}
	// The expiry is when tracing silently stops: every export after it is a
	// 401. The person must hear it from the agent, because nothing else on
	// their machine will tell them.
	expiry := "It does not expire, so tracing keeps working until the key is revoked. Tell the person that."
	if created.ExpiresAt != "" {
		expiry = fmt.Sprintf("It expires at %s. Tell the person that tracing will stop then, and that a new tracing key "+
			"(create_ingest_key again, or the monitor's Connect page) written into the same file restarts it.", created.ExpiresAt)
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Tracing key created (id=%s, name=%q, prefix=%s, scope=%s, monitor=%s, expires_at=%s).\n\n"+
			"It can send telemetry and pings for that one monitor and nothing else. Write it straight into the git-ignored "+
			"file or helper script the set-up names; do not repeat it to the person and do not put it in committed code.\n\n"+
			"%s\n\n"+
			"PLAINTEXT KEY (shown ONCE, it cannot be retrieved again):\n%s",
		created.ID, created.Name, created.Prefix, created.Scope, created.CheckID, expiresOrNever(created.ExpiresAt), expiry, created.Key)), nil
}

// expiresOrNever is an expires_at for the one-line summary.
func expiresOrNever(at string) string {
	if at == "" {
		return "never"
	}
	return at
}

// marshalNoEscape renders v as indented JSON with HTML escaping off, so
// "<your tracing key>" and shell `&&` stay paste-ready (see marshalSnippets).
func marshalNoEscape(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		out, _ := json.MarshalIndent(v, "", "  ")
		return string(out)
	}
	return strings.TrimRight(buf.String(), "\n")
}
