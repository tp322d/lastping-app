package mcptools

// run_expectations.go — the declare_run_expectations tool: a pure proxy to
// POST /api/v1/checks/{id}/runs/{rid}/expectations (api/run_assertions.go).
//
// WHY a separate file from assertions.go: assertions.go's PUT
// /api/v1/checks/{id}/assertions is a per-MONITOR, replace-the-set,
// human-authored resource (each entry carries a `name`). This tool declares a
// per-RUN, declare-ONCE, agent-authored set that has no name at all — see
// api/run_assertions.go's runAssertionDTO doc comment for why a run
// assertion carries no name column and never will. Keeping the two in
// separate files is what keeps that difference visible rather than inviting
// exactly the drift Task 2's review flagged: code that "helpfully"
// synthesises a name for a run assertion when there is none to synthesise.
//
// This tool does NO validation of its own — it decodes the `assertions` JSON
// argument only far enough to shape the request body, and lets the API be
// the single source of truth for what a valid assertion is
// (core/assertion.Validate, called server-side, on every write). That keeps
// this file a pure proxy, consistent with every other tool in this package,
// and is why agentprompt_boundary_test.go's import guarantee is untouched by
// this file: nothing here reasons about prompts or validity, only about
// shuttling JSON to and from one HTTP endpoint.
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
// wire shape POST .../runs/{rid}/expectations accepts, matching
// api/run_assertions.go's runAssertionDTO field-for-field. Deliberately no
// `name` and no `id`: see this file's package doc comment.
type runExpectationDeclaration struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`
	Op    string `json:"op,omitempty"`
}

// declareRunExpectationsDesc is the `assertions` argument's description.
const declareRunExpectationsDesc = "The run's complete set of expectations, declared ONCE at the start of the run -- criteria the ping BODY of " +
	"THIS run's eventual success ping must satisfy when the run closes, checked instead of letting the run grade itself. " +
	"IMMUTABLE: a second call for the same rid is rejected with a conflict error and the first declaration stands unchanged -- there is no way " +
	"to edit, add to, or replace it once made, so decide the whole set before you start work. Declaring nothing is allowed and always has been: " +
	"simply never call this tool for a run, and the monitor's own check-level assertions (if any) stay in force unchanged. " +
	"INCLUDE AT LEAST ONE POSITIVE CRITERION -- a 'contains', 'matches' or 'json_path' entry -- in every declaration. A declaration made ENTIRELY " +
	"of 'not_contains' entries is self-satisfying on empty output: a run that produces nothing at all still passes, because there is nothing for " +
	"the pattern to find. That is precisely the evasion this feature exists to close, so a purely negative declaration defeats its own purpose. " +
	"A 'matches' entry only counts as positive if its pattern REJECTS an empty body: '.*', '(?s).*' and '^$' all accept one and are validated as " +
	"perfectly legal patterns, so a declaration resting on one of those is no better than a purely negative declaration. " +
	"Supply a JSON ARRAY as a string, e.g. " +
	`'[{"kind":"json_path","path":"result.rows_processed","op":"gt","value":"0"}]'` + ". " +
	"Fields per entry: kind (required), value, path, op -- no name; a run's declared criteria have none, unlike a monitor's own output assertions. " +
	"kind is one of 'contains' (body contains value as a substring), 'not_contains' (body does not contain it), " +
	"'matches' (body matches value as a Go RE2 regexp, max 1000 bytes), or 'json_path' (parse the body as JSON, read the value at path, compare it against value with op). " +
	"contains/not_contains/matches require value; json_path requires path and op. " +
	"path is a DOTTED path only ('a.b.c') -- the query syntax of a real JSONPath library ('[', '*', '$') is rejected. " +
	"op is one of 'eq', 'ne', 'gt', 'gte', 'lt', 'lte'. " +
	"At most 20 assertions per run. A malformed entry (uncompilable regexp, a path carrying query syntax, an unknown kind or op) is rejected " +
	"before anything is written, and nothing is stored if any entry fails."

// registerRunExpectationTools registers declare_run_expectations.
func registerRunExpectationTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("declare_run_expectations",
			mcp.WithDescription("Commit, at the START of a run, to the criteria by which THAT RUN will be judged when it closes — before you can "+
				"see how it turns out. This is how a run stops grading itself: once declared, a success ping whose body does not satisfy every "+
				"declared criterion is recorded as a FAILED run with cause 'assertion', regardless of the exit code or what the ping claims. "+
				"Call this right after your run's /start ping, before doing any work — see the assertions argument for the full, immutable "+
				"contract, and get_ping_instructions' expectations_how_to for a worked example."),
			mcp.WithString("check_id", mcp.Required(), mcp.Description("Monitor UUID (from create_monitor or list_monitors).")),
			mcp.WithString("rid", mcp.Required(), mcp.Description("The run id exactly as sent on this run's /start ping — the same rid used on every step and the terminal ping.")),
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
// left to the API's own core/assertion.Validate call, not duplicated here.
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
