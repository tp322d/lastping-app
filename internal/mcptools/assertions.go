package mcptools

// assertions.go — the `assertions` argument shared by update_monitor, and the
// GET/PUT /api/v1/checks/{id}/assertions sub-resource it round-trips through.
//
// This client does NOT validate assertion contents (kind, path syntax,
// regexp compilability, the per-monitor count cap) before sending them. The
// LastPing API validates authoritatively — REST, Terraform and this MCP
// client all send the same payload shape to the same endpoint — so a
// malformed assertion comes back as a normal API error instead of a local
// one. The rules are still documented in assertionsDesc below, because the
// description is published even though the validating code is not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MonitorAssertion is one output assertion as it travels over the management
// API's /api/v1/checks/{id}/assertions endpoint. The field names and the
// omitempty choices mirror the API's assertion DTO exactly, so an agent can
// round-trip what get_monitor printed straight back into update_monitor's
// `assertions` argument without editing it.
type MonitorAssertion struct {
	// ID is server-assigned and read-only: the endpoint replaces the whole set
	// on every write, so an id sent back in is ignored rather than honoured.
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`
	Op    string `json:"op,omitempty"`
}

// assertionsEnvelope is the request/response body of GET and PUT
// /api/v1/checks/{id}/assertions.
type assertionsEnvelope struct {
	Assertions []MonitorAssertion `json:"assertions"`
}

// assertionsDesc is the `assertions` argument's description. It is a single
// constant so update_monitor's parameter text and any future tool that accepts
// the same argument cannot drift into two accounts of replace-the-set
// semantics.
const assertionsDesc = "Output assertions: conditions the ping BODY of a successful run must satisfy, checked on every success ping. " +
	"This is how you catch the job that exits zero having done nothing — a backup that wrote no rows, an export that produced an empty file. " +
	"When an assertion fails, the success ping opens an incident with cause 'assertion' naming the assertion that did not hold, exactly as a real failure would. " +
	"Supply a JSON ARRAY as a string, e.g. " +
	`'[{"name":"rows written","kind":"json_path","path":"result.rows_processed","op":"gt","value":"0"}]'` + ". " +
	"REPLACE-THE-SET: the array you send becomes the monitor's complete assertion set — it is NOT merged with what is already there. " +
	"Omit the argument entirely to leave the current assertions untouched; pass '[]' to remove all of them. " +
	"Fields per entry: name (required, appears in the alert), kind (required), value, path, op. " +
	"kind is one of 'contains' (body contains value as a substring), 'not_contains' (body does not contain it), " +
	"'matches' (body matches value as a Go RE2 regexp, max 1000 bytes), or 'json_path' (parse the body as JSON, read the value at path, compare it against value with op). " +
	"contains/not_contains/matches require value; json_path requires path and op and ignores them otherwise. " +
	"path is a DOTTED path only ('a.b.c') — the query syntax of a real JSONPath library ('[', '*', '$') is rejected. " +
	"op is one of 'eq', 'ne', 'gt', 'gte', 'lt', 'lte'. " +
	"Comparison rule for json_path: when BOTH the value read from the body and the value you supplied parse as numbers the comparison is numeric, otherwise both sides are compared as strings — " +
	"so with op 'gt', value '3' beats '12.5' lexically but loses numerically, and 'rows_processed gt 0' means what it looks like it means. " +
	"At most 20 assertions per monitor. A malformed entry (uncompilable regexp, a path carrying query syntax, an unknown kind or op) is rejected before anything is written and names the offending assertion."

// parseAssertionsArg decodes the `assertions` tool argument.
//
// present is false when the argument was absent or an empty string, which is
// the "leave the monitor's assertions alone" case — distinct from an explicit
// "[]", which clears them and comes back as present with an empty (non-nil)
// slice.
//
// This performs no semantic validation (kind, path syntax, regexp
// compilability, the 20-per-monitor cap) — only enough parsing to build the
// PUT body. The API is the sole authority on whether the contents are valid;
// a malformed entry comes back as a normal API error.
func parseAssertionsArg(raw string) (as []MonitorAssertion, present bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}

	var parsed []MonitorAssertion
	if uErr := json.Unmarshal([]byte(raw), &parsed); uErr != nil {
		return nil, false, fmt.Errorf("assertions must be a JSON array, e.g. "+
			`[{"name":"rows written","kind":"json_path","path":"result.rows","op":"gt","value":"0"}]`+": %v", uErr)
	}
	if parsed == nil {
		// Literal `null` decodes to a nil slice. Treat it as the empty set so
		// the PUT body carries [] rather than null.
		parsed = []MonitorAssertion{}
	}
	return parsed, true, nil
}

// getAssertions reads the current assertion set for a monitor. It returns the
// set and a nil error on 200; any other status is reported as an error.
func (c *APIClient) getAssertions(ctx context.Context, id string) ([]MonitorAssertion, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/assertions", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.problem(resp)
	}

	var env assertionsEnvelope
	if dErr := json.NewDecoder(resp.Body).Decode(&env); dErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", dErr)
	}
	return env.Assertions, nil
}

// putAssertions replaces a monitor's whole assertion set. as must be non-nil;
// an empty slice clears the set.
func (c *APIClient) putAssertions(ctx context.Context, id string, as []MonitorAssertion) ([]MonitorAssertion, error) {
	if as == nil {
		as = []MonitorAssertion{}
	}
	data, err := json.Marshal(assertionsEnvelope{Assertions: as})
	if err != nil {
		return nil, fmt.Errorf("failed to encode assertions: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/v1/checks/"+id+"/assertions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.problem(resp)
	}

	var env assertionsEnvelope
	if dErr := json.NewDecoder(resp.Body).Decode(&env); dErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", dErr)
	}
	return env.Assertions, nil
}
