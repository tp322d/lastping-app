package mcptools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// APIClient is a thin authenticated HTTP client for the LastPing management API.
type APIClient struct {
	BaseURL string
	apiKey  string
	HTTP    *http.Client
}

// NewAPIClient creates an APIClient. baseURL must NOT have a trailing slash.
// apiKey is sent as "Authorization: Bearer <key>" on every request.
func NewAPIClient(baseURL, apiKey string) *APIClient {
	return &APIClient{
		BaseURL: baseURL,
		apiKey:  apiKey,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// problem reads an RFC 7807 Problem response and returns a human-readable error.
// If the body cannot be decoded it falls back to the HTTP status text.
//
// max_scope and required_scope are the API's scope-refusal extension members,
// and they are FOLDED into the sentence rather than dropped. This error string
// is the only thing that reaches the tool result — the MCP surface shows a
// problem's detail and nothing else — so a refusal that names neither the
// ceiling nor the floor tells the agent only that it was refused, not what to
// do next:
//
//   - max_scope comes back on the 400 from POST /api/v1/api-keys when the
//     requested scope is higher than the creating key's own. It is the highest
//     scope this caller could grant a child.
//   - required_scope comes back on a 403 from any scoped route. It is the
//     minimum scope that route needed.
//
// Only one can be present on a given response, so the branches are ordered
// rather than combined.
func (c *APIClient) problem(resp *http.Response) error {
	_, err := c.problemDetail(resp)
	return err
}

// problemInfo is the machine-readable part of an RFC 7807 body: the fields a
// client may BRANCH on, as opposed to the sentence it shows a human. Each is
// an empty string when the body did not decode or did not carry it.
type problemInfo struct {
	// Detail is the problem's `detail` member.
	Detail string
	// MaxExpiresAt is the `max_expires_at` extension member the API sets on
	// the api-keys expiry cap refusal: the latest expires_at this caller could
	// have asked for, RFC 3339.
	MaxExpiresAt string
	// MaxScope and RequiredScope are the scope-refusal members described on
	// problem above.
	MaxScope      string
	RequiredScope string
}

// problemDetail reads an RFC 7807 Problem response and returns its
// machine-readable fields alongside the human-readable error problem would
// have produced. A response body can be read exactly once, so a caller that
// must both BRANCH on the body and still be able to report the failure has to
// obtain the two together: see mintKey, which recovers from one specific
// detail using the ceiling that comes with it, and delete_route, which tells
// two different 404s apart.
func (c *APIClient) problemDetail(resp *http.Response) (problemInfo, error) {
	defer resp.Body.Close()
	var p struct {
		Title         string `json:"title"`
		Detail        string `json:"detail"`
		Status        int    `json:"status"`
		MaxExpiresAt  string `json:"max_expires_at"`
		MaxScope      string `json:"max_scope"`
		RequiredScope string `json:"required_scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return problemInfo{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	info := problemInfo{Detail: p.Detail, MaxExpiresAt: p.MaxExpiresAt, MaxScope: p.MaxScope, RequiredScope: p.RequiredScope}
	if p.Detail != "" {
		if p.MaxScope != "" {
			return info, fmt.Errorf("%s (HTTP %d): %s (max_scope: %s)", p.Title, p.Status, p.Detail, p.MaxScope)
		}
		if p.RequiredScope != "" {
			return info, fmt.Errorf("%s (HTTP %d): %s (required_scope: %s)", p.Title, p.Status, p.Detail, p.RequiredScope)
		}
		return info, fmt.Errorf("%s (HTTP %d): %s", p.Title, p.Status, p.Detail)
	}
	return info, fmt.Errorf("%s (HTTP %d)", p.Title, p.Status)
}

// auth adds the Authorization header to a request.
func (c *APIClient) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}
