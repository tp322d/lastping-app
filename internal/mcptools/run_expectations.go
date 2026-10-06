package mcptools

// run_expectations.go — the declare_run_expectations tool: a pure proxy to
// POST /api/v1/checks/{id}/runs/{rid}/expectations.
//
// WHY a separate file from assertions.go: assertions.go's PUT
// /api/v1/checks/{id}/assertions is a per-MONITOR, replace-the-set,
// human-authored resource (each entry carries a `name`). This tool declares a
// per-RUN, declare-ONCE, agent-authored set that has no name at all: the API
// stores no name for a run assertion. Keeping the two in separate files is
// what keeps that difference visible rather than inviting code that
// "helpfully" synthesises a name for a run assertion when there is none to
// synthesise.
//
// This tool does NO validation of its own — it decodes the `assertions` JSON
// argument only far enough to shape the request body, and lets the API be
// the single source of truth for what a valid assertion is
// (validated server-side, on every write). That keeps this file a pure proxy,
// consistent with every other tool in this package: nothing here reasons
// about prompts or validity, only about shuttling JSON to and from one HTTP
// endpoint.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// runExpectationDeclaration is one entry of the `assertions` argument — the
// wire shape POST .../runs/{rid}/expectations accepts, field for field. Deliberately no
// `name` and no `id`: see this file's package doc comment.
type runExpectationDeclaration struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`
	Op    string `json:"op,omitempty"`
}

// declareRunExpectationsDesc is the `assertions` argument's description.
const declareRunExpectationsDesc = "The run's complete criteria, as a JSON array string, e.g. " +
	`'[{"kind":"json_path","path":"result.rows_processed","op":"gt","value":"0"}]'` + ". " +
	"Fields: kind (required), value, path, op; no name. kind: contains, not_contains, matches (RE2, max 1000 bytes) or " +
	"json_path (the value at a dotted path, e.g. 'a.b.c', compared with op: eq, ne, gt, gte, lt, lte). " +
	"Only not_contains entries, or a matches pattern that accepts an empty body ('.*', '^$'), pass on empty output; " +
	"a contains, json_path or empty-rejecting matches entry catches a run that produced nothing. " +
	"At most 20; one malformed entry rejects the whole declaration. Runs with no declaration keep the monitor's own assertions."

// registerRunExpectationTools registers declare_run_expectations.
func registerRunExpectationTools(s *server.MCPServer) {
	s.AddTool(
		newTool("declare_run_expectations",
			mcp.WithDescription("Declares, at the start of a run, the criteria its success ping body is judged by when the run closes, so the run does not grade itself: "+
				"a success whose body fails any declared criterion is recorded as a failed run with cause 'assertion', whatever the exit code. "+
				"One declaration per rid, immutable (a second call is a conflict). For use right after the run's /start ping; "+
				"get_ping_instructions' expectations_how_to has a worked example."),
			mcp.WithString("check_id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
			mcp.WithString("rid", mcp.Required(), mcp.Description("The run id exactly as sent on this run's /start ping, the same rid used on every step and the terminal ping.")),
			mcp.WithString("assertions", mcp.Required(), mcp.Description(declareRunExpectationsDesc)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			checkID, err := req.RequireString("check_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rid, err := req.RequireString("rid")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := req.RequireString("assertions")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.declareRunExpectations(ctx, checkID, rid, raw)
		},
	)
}

// declareRunExpectations proxies to
// POST /api/v1/checks/{id}/runs/{rid}/expectations. rawAssertions is decoded
// only to shape the outgoing JSON body — every semantic rule (required
// fields per kind, regexp validity, dotted-path syntax, the 20-entry cap) is
// left to the API's own validation, not duplicated here.
func (c *APIClient) declareRunExpectations(ctx context.Context, checkID, rid, rawAssertions string) (*mcp.CallToolResult, error) {
	raw := strings.TrimSpace(rawAssertions)
	var entries []runExpectationDeclaration
	if uErr := json.Unmarshal([]byte(raw), &entries); uErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("assertions must be a JSON array, e.g. "+
			`[{"kind":"json_path","path":"result.rows_processed","op":"gt","value":"0"}]`+": %v", uErr)), nil
	}

	data, err := json.Marshal(map[string]any{"assertions": entries})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode assertions: %v", err)), nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/checks/"+checkID+"/runs/"+rid+"/expectations", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return mcp.NewToolResultError(fmt.Sprintf(
			"Run not found: check_id=%s rid=%s. Send a /start ping for this rid before declaring expectations.", checkID, rid)), nil
	case http.StatusConflict:
		return mcp.NewToolResultError(fmt.Sprintf(
			"Cannot declare expectations for rid=%s: %v. Declarations are made ONCE — either this run already has a declared set, "+
				"or it has already closed.", rid, c.problem(resp))), nil
	case http.StatusCreated:
		// falls through to the decode below
	default:
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var env struct {
		Assertions []runExpectationDeclaration `json:"assertions"`
	}
	if dErr := json.NewDecoder(resp.Body).Decode(&env); dErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", dErr)), nil
	}

	out, mErr := json.MarshalIndent(env, "", "  ")
	if mErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to render response: %v", mErr)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Declared %d expectation(s) for run %s, cannot be changed:\n%s", len(env.Assertions), rid, string(out))), nil
}
