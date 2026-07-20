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
func (c *APIClient) problem(resp *http.Response) error {
	defer resp.Body.Close()
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if p.Detail != "" {
		return fmt.Errorf("%s (HTTP %d): %s", p.Title, p.Status, p.Detail)
	}
	return fmt.Errorf("%s (HTTP %d)", p.Title, p.Status)
}

// auth adds the Authorization header to a request.
func (c *APIClient) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}
