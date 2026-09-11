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
	defer resp.Body.Close()
	var p struct {
		Title         string `json:"title"`
		Detail        string `json:"detail"`
		Status        int    `json:"status"`
		MaxScope      string `json:"max_scope"`
		RequiredScope string `json:"required_scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if p.Detail != "" {
		if p.MaxScope != "" {
			return fmt.Errorf("%s (HTTP %d): %s (max_scope: %s)", p.Title, p.Status, p.Detail, p.MaxScope)
		}
		if p.RequiredScope != "" {
			return fmt.Errorf("%s (HTTP %d): %s (required_scope: %s)", p.Title, p.Status, p.Detail, p.RequiredScope)
		}
		return fmt.Errorf("%s (HTTP %d): %s", p.Title, p.Status, p.Detail)
	}
	return fmt.Errorf("%s (HTTP %d)", p.Title, p.Status)
}

// auth adds the Authorization header to a request.
func (c *APIClient) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}
