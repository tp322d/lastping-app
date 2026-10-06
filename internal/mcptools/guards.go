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
const guardsDesc = "Metric guards: ceilings on a number the job reports, checked on every ping; a total above the ceiling (equal does not trip) opens a 'runaway' incident. " +
	"A JSON array as a string, e.g. " +
	`'[{"name":"daily spend","path":"cost.usd","window_s":86400,"ceiling":50,"aggregation":"sum"}]'` + ". " +
	"The array replaces the whole set; '[]' removes all, omitted leaves it. " +
	"Every entry needs name, path (dotted), window_s (trailing seconds), ceiling and aggregation ('sum', 'max' or 'avg'). " +
	"Pings with no number at the path are skipped, not counted as zero. " +
	"At most 5 guards, window_s at most 604800 (7 days); " +
	"a malformed entry is rejected by name; the set stays as is."

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
