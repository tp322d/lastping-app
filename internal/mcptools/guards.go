package mcptools

// guards.go — the `guards` argument shared by update_monitor, and the
// GET/PUT /api/v1/checks/{id}/guards sub-resource it round-trips through.
//
// Like assertions.go, this client does NOT validate guard contents (path
// syntax, the per-monitor count cap, the window cap) before sending them —
// the API is the authoritative validator, and the rules are documented in
// guardsDesc below rather than enforced here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MonitorGuard is one metric guard as it travels over the management API's
// /api/v1/checks/{id}/guards endpoint. The field names and the omitempty
// choices mirror the API's guard DTO exactly, so an agent can round-trip what
// get_monitor printed straight back into update_monitor's `guards` argument
// without editing it.
type MonitorGuard struct {
	// ID is server-assigned and read-only: the endpoint replaces the whole set
	// on every write, so an id sent back in is ignored rather than honoured.
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	WindowS     int64   `json:"window_s"`
	Ceiling     float64 `json:"ceiling"`
	Aggregation string  `json:"aggregation"`
}

// guardsEnvelope is the request/response body of GET and PUT
// /api/v1/checks/{id}/guards.
type guardsEnvelope struct {
	Guards []MonitorGuard `json:"guards"`
}

// guardsDesc is the `guards` argument's description. Like assertionsDesc it
// is a single constant so update_monitor's parameter text and any future tool
// accepting the same argument cannot drift into two accounts of
// replace-the-set semantics. The 5-guard and 604800-second (7-day) caps are
// restated here as literals rather than imported constants: the API enforces
// them, this client only documents them.
const guardsDesc = "Metric guards: CEILINGS on a number the job reports about itself, checked on every ping. " +
	"An assertion catches a run that did nothing; a guard catches the opposite — an agent that loops, retries and burns money. " +
	"Each guard reads one number out of the ping body at a dotted path, rolls it up across a trailing window, " +
	"and opens an incident with cause 'runaway' when the total EXCEEDS the ceiling (equal does not trip). " +
	"Supply a JSON ARRAY as a string, e.g. " +
	`'[{"name":"daily spend","path":"cost.usd","window_s":86400,"ceiling":50,"aggregation":"sum"}]'` + ". " +
	"REPLACE-THE-SET: the array you send becomes the monitor's complete guard set — it is NOT merged with what is already there. " +
	"Omit the argument entirely to leave the current guards untouched; pass '[]' to remove all of them. " +
	"Fields per entry, all required: name (appears on the incident, and is the only thing that tells a tripped guard apart from the " +
	"fixed pings-per-hour runaway ceiling), path (DOTTED path into the ping body parsed as JSON — 'cost.usd'; the query syntax of a " +
	"real JSONPath library ('[', '*', '$') is rejected, exactly as for an assertion's path), window_s (trailing window in seconds), " +
	"ceiling (number), aggregation (one of 'sum', 'max', 'avg'). " +
	"Pings whose body is missing, is not JSON, or carries nothing numeric at that path are SKIPPED, not counted as zero — " +
	"so a `start` ping never drags an average down. " +
	"At most 5 guards per monitor, and window_s at most 604800 seconds (7 days). " +
	"Both caps are cost, not policy: a guard re-aggregates every ping body in its window on every ping, so the per-ping work is " +
	"linear in BOTH the window and the number of guards (measured: 4.2 ms/ping at a 1-hour window, 390 ms/ping at 30 days). " +
	"A window longer than the 90-day ping retention would also aggregate over already-pruned rows and quietly under-report. " +
	"A malformed entry is rejected before anything is written and names the offending guard."

// parseGuardsArg decodes the `guards` tool argument.
//
// present is false when the argument was absent or an empty string — "leave
// the monitor's guards alone" — as distinct from an explicit "[]", which
// clears them and comes back as present with an empty (non-nil) slice.
//
// This performs no semantic validation (the 5-per-monitor cap, the 7-day
// window cap, path syntax) — only enough parsing to build the PUT body. The
// API is the sole authority on whether the contents are valid.
func parseGuardsArg(raw string) (gs []MonitorGuard, present bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}

	var parsed []MonitorGuard
	if uErr := json.Unmarshal([]byte(raw), &parsed); uErr != nil {
		return nil, false, fmt.Errorf("guards must be a JSON array, e.g. "+
			`[{"name":"daily spend","path":"cost.usd","window_s":86400,"ceiling":50,"aggregation":"sum"}]`+": %v", uErr)
	}
	if parsed == nil {
		// Literal `null` decodes to a nil slice. Treat it as the empty set so
		// the PUT body carries [] rather than null.
		parsed = []MonitorGuard{}
	}
	return parsed, true, nil
}

// getGuards reads the current metric-guard set for a monitor. It returns the
// set and a nil error on 200; any other status is reported as an error.
func (c *APIClient) getGuards(ctx context.Context, id string) ([]MonitorGuard, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/guards", nil)
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

	var env guardsEnvelope
	if dErr := json.NewDecoder(resp.Body).Decode(&env); dErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", dErr)
	}
	return env.Guards, nil
}

// putGuards replaces a monitor's whole metric-guard set. gs must be non-nil;
// an empty slice clears the set.
func (c *APIClient) putGuards(ctx context.Context, id string, gs []MonitorGuard) ([]MonitorGuard, error) {
	if gs == nil {
		gs = []MonitorGuard{}
	}
	data, err := json.Marshal(guardsEnvelope{Guards: gs})
	if err != nil {
		return nil, fmt.Errorf("failed to encode guards: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/v1/checks/"+id+"/guards", bytes.NewReader(data))
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

	var env guardsEnvelope
	if dErr := json.NewDecoder(resp.Body).Decode(&env); dErr != nil {
		return nil, fmt.Errorf("failed to decode response: %w", dErr)
	}
	return env.Guards, nil
}
