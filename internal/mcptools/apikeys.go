package mcptools

// apikeys.go — MCP tools for API key management.
//
// SECURITY: create_api_key returns the plaintext key in its tool result. That is
// the only way to deliver it, but it means the value enters the agent's
// conversation — the tool description tells the caller to store it immediately
// and prefer a short expires_at.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// APIKey mirrors the LastPing ApiKey resource (never the plaintext key).
//
// LastUsedSurface is best-effort client self-identification derived from the
// caller-controlled, spoofable User-Agent header at auth time. It answers
// "did my client ever successfully authenticate?" and nothing more — never
// treat it as proof of a current session or as any form of authorization
// signal.
type APIKey struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Prefix          string `json:"prefix"`
	CreatedAt       string `json:"created_at"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	LastUsedSurface string `json:"last_used_surface,omitempty"`
	// Scope is the key's authorization tier: "read", "write" or "admin".
	// Unlike LastUsedSurface above, this IS an authorization fact — it is what
	// the server checks, not what a client claimed about itself.
	Scope string `json:"scope,omitempty"`
	// CreatedByKeyID is the key that minted this one, absent for a key minted
	// from the dashboard. Revoking a key revokes everything below it in this
	// chain.
	CreatedByKeyID string `json:"created_by_key_id,omitempty"`
}

func registerAPIKeyTools(s *server.MCPServer) {
	s.AddTool(
		newTool("create_api_key",
			mcp.WithDescription("Create a new LastPing API key. The plaintext key is returned "+
				"ONCE and cannot be retrieved again — store it immediately in a secret manager. "+
				"Set expires_at for a short-lived key."),
			mcp.WithString("name", mcp.Required(),
				mcp.Description("Label for the key, e.g. \"github-actions\".")),
			mcp.WithString("expires_at",
				mcp.Description("Optional RFC 3339 expiry, e.g. \"2026-12-31T00:00:00Z\". "+
					"Omit for a key that never expires. A key can never be given a longer "+
					"life than the key that creates it.")),
			mcp.WithString("scope",
				mcp.Enum("read", "write", "admin"),
				mcp.Description("What the new key may do. \"read\" is every GET; \"write\" is "+
					"everything except managing API keys; \"admin\" is everything, key "+
					"management included. Omit for \"write\", which is the right tier for a "+
					"credential handed to a job or an agent: it can do the work and cannot "+
					"mint itself a replacement. A key can never be given a HIGHER scope than "+
					"the key that creates it; asking for one is refused and the refusal names "+
					"the ceiling.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createAPIKey(ctx, name, req.GetString("expires_at", ""), req.GetString("scope", ""))
		},
	)

	s.AddTool(
		newTool("list_api_keys",
			mcp.WithDescription("List all API keys in the project. Never returns plaintext key "+
				"values — only the non-secret prefix, which is enough to identify a key for "+
				"revoke_api_key. Each key includes last_used_at and last_used_surface (which "+
				"client — \"mcp\", \"terraform\", or \"api\" — most recently authenticated with "+
				"it), both absent if the key has never been used, plus scope (\"read\", "+
				"\"write\" or \"admin\" — what the key is permitted to do) and "+
				"created_by_key_id (which key minted it, absent for a key made in the "+
				"dashboard; revoking a key also revokes every key below it in that chain). "+
				"last_used_surface is best-effort client self-identification from a "+
				"caller-controlled, spoofable User-Agent header: useful for answering "+
				"\"did my client ever successfully authenticate?\", never a basis for trust "+
				"or authorization decisions.")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listAPIKeys(ctx)
		},
	)

	s.AddTool(
		newTool("revoke_api_key",
			mcp.WithDescription("Permanently revoke an API key AND every key it created, "+
				"recursively: the keys that key made, the keys those keys made, all the way "+
				"down. All of them stop authenticating immediately. Revoking cascades because "+
				"a key that can mint keys would otherwise outlive its own revocation. Check "+
				"list_api_keys first — created_by_key_id shows which keys hang off this one — "+
				"because this cannot be undone and may revoke more than one credential."),
			mcp.WithString("api_key_id", mcp.Required(),
				mcp.Description("UUID of the key to revoke. Get it from list_api_keys.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("api_key_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.revokeAPIKey(ctx, id)
		},
	)
}

// createAPIKey issues POST /api/v1/api-keys. expiresAt is RFC 3339, or an
// empty string for a key that never expires.
//
// An empty scope omits the field entirely rather than sending an empty string,
// which is what asks the server to apply ITS OWN default (currently "write").
// Sending a default from here would put that policy in two places, and the
// server's default is already the safe one — unlike a scope the agent asked
// for and was refused, which is surfaced rather than quietly retried a tier
// lower: substituting a scope the caller did not ask for would be the tool
// editing its caller's intent.
func (c *APIClient) createAPIKey(ctx context.Context, name, expiresAt, scope string) (*mcp.CallToolResult, error) {
	payload := map[string]any{"name": name}
	if expiresAt != "" {
		payload["expires_at"] = expiresAt
	}
	if scope != "" {
		payload["scope"] = scope
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode request: %v", err)), nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/api-keys", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var created struct {
		APIKey
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"API key created (id=%s, name=%q, prefix=%s, scope=%s).\n\n"+
			"PLAINTEXT KEY (shown ONCE — store it now, it cannot be retrieved again):\n%s",
		created.ID, created.Name, created.Prefix, created.Scope, created.Key)), nil
}

func (c *APIClient) listAPIKeys(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/api-keys", nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var keys []APIKey
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(keys) == 0 {
		return mcp.NewToolResultText("No API keys found. Create one with create_api_key."), nil
	}

	out, _ := json.MarshalIndent(keys, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) revokeAPIKey(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/api-keys/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("API key not found: id=%s. Use list_api_keys to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	// X-Revoked-Count is how many keys went: this one plus its descendants.
	// The API answers 204 with no body, so the header is the only place the
	// number exists — and an agent that has just destroyed several credentials
	// needs to be told, not left to infer it from a later list_api_keys.
	if n, err := strconv.Atoi(resp.Header.Get("X-Revoked-Count")); err == nil && n > 1 {
		return mcp.NewToolResultText(fmt.Sprintf(
			"API key %s revoked, along with %d key(s) it created — %d in total.",
			id, n-1, n)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("API key %s revoked.", id)), nil
}
