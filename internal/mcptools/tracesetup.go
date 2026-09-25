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
var traceSetupTools = []string{"claude-code", "codex", "gemini", "cursor", "python", "node", "otel-sdk", "collector"}

func registerTraceSetupTools(s *server.MCPServer) {
	s.AddTool(
		newTool("get_trace_setup",
			mcp.WithDescription("Get the exact steps to make a tool send OpenTelemetry traces to LastPing for one monitor: what to write, where, "+
				"how to verify it, and what that tool cannot do. Call this when a person asks you to set up tracing, observability or telemetry for "+
				"a project. Carry the steps out yourself rather than printing them: write the files, check they are git ignored, send the test span, "+
				"and report the console link. Never echo the credential back to the person and never put it in committed code. "+
				"The result's `prompt` is the full instruction for the tool you name, and each block's `files` are what to write; get the key with "+
				"create_ingest_key."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
			mcp.WithString("tool",
				mcp.Enum(traceSetupTools...),
				mcp.Description("Which tool will send the traces: claude-code, codex, gemini, cursor, python, node, otel-sdk or collector. Omit to get every block."))),
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
			mcp.WithDescription("Create a tracing key for one monitor: an ingest-scoped LastPing key bound to that monitor. It can send traces, metrics, "+
				"logs and pings for that one monitor and nothing else; it cannot read or change anything in the account, and every REST call refuses it. "+
				"This is the credential get_trace_setup's steps need. The plaintext key is returned ONCE: write it straight into the git-ignored file or "+
				"helper script the set-up names, never into committed code, and never echo it back to the person, in a reply, a commit message or a log. "+
				"Never use your own LastPing API key as an exporter's credential instead. Omit expires_at for a 90-day key, capped at your own key's expiry."),
			mcp.WithString("monitor_id", mcp.Required(), mcp.Description("Monitor UUID the key is bound to (from create_monitor or list_monitors).")),
			mcp.WithString("name", mcp.Description("Optional label for the key. Defaults to \"Tracing:\" followed by the monitor's name.")),
			mcp.WithString("expires_at", mcp.Description("Optional RFC 3339 expiry, e.g. \"2026-12-31T00:00:00Z\". Omit for a 90-day key, capped at the "+
				"creating key's own expiry."))),
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
