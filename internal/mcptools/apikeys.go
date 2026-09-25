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
	"net/url"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// mcpDefaultKeyExpiry is how far out an MCP-created key expires when the
// agent omits expires_at. REST itself still treats an omitted value as
// "never" (existing Terraform configurations depend on that contract), but an
// agent calling this tool almost never sets expires_at at all, so leaving the
// default to REST would mint a permanent credential by omission on the one
// surface a human is least likely to notice it on. Matches the console's
// 90-day default and the hosted server at mcp.lastping.dev.
const mcpDefaultKeyExpiry = 90 * 24 * time.Hour

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
	// Scope is the key's authorization tier: "read", "write" or "admin", or
	// "ingest" (telemetry and pings only, never the REST API).
	// Unlike LastUsedSurface above, this IS an authorization fact — it is what
	// the server checks, not what a client claimed about itself.
	Scope string `json:"scope,omitempty"`
	// CreatedByKeyID is the key that minted this one, absent for a key minted
	// from the dashboard. Revoking a key revokes everything below it in this
	// chain.
	CreatedByKeyID string `json:"created_by_key_id,omitempty"`
	// CheckID is the one monitor an ingest key is bound to, absent for an
	// unbound key and for every other scope.
	CheckID string `json:"check_id,omitempty"`
}

func registerAPIKeyTools(s *server.MCPServer) {
	registerRegenerateAPIKeyTool(s)
	s.AddTool(
		newTool("create_api_key",
			mcp.WithDescription("Create a new LastPing API key. The plaintext key is returned "+
				"ONCE and cannot be retrieved again — store it immediately in a secret manager. "+
				"Set expires_at for a short-lived key. To give an exporter a tracing key for one monitor, "+
				"use create_ingest_key instead: it needs only a write key."),
			mcp.WithString("name", mcp.Required(),
				mcp.Description("Label for the key, e.g. \"github-actions\".")),
			mcp.WithString("expires_at",
				mcp.Description("Optional RFC 3339 expiry, e.g. \"2026-12-31T00:00:00Z\". "+
					"Omit for a 90-day key, capped at the creating key's own expiry. "+
					"A key can never be given a longer life than the key that creates it.")),
			mcp.WithString("scope",
				mcp.Enum("read", "write", "admin", "ingest"),
				mcp.Description("What the new key may do. \"read\" is every GET; \"write\" is "+
					"everything except managing API keys; \"admin\" is everything, key "+
					"management included. Omit for \"write\", which is the right tier for a "+
					"credential handed to a job or an agent: it can do the work and cannot "+
					"mint itself a replacement. A key can never be given a HIGHER scope than "+
					"the key that creates it; asking for one is refused and the refusal names "+
					"the ceiling. \"ingest\" can only send pings and telemetry (traces, "+
					"metrics, logs) and cannot call the REST API at all: use it for a key that "+
					"lives in a dotfile or an exporter's config. This tool needs an admin key; "+
					"with a write key, use create_ingest_key, which mints a tracing key bound to one monitor.")),
			mcp.WithString("check_id",
				mcp.Description("Only with scope \"ingest\": binds the key to this one monitor (UUID from "+
					"list_monitors), so it can send telemetry for that monitor and nothing else. "+
					"Required for an exporter that cannot name its monitor, such as Codex.")),
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
			return c.createAPIKey(ctx, name, req.GetString("expires_at", ""), req.GetString("scope", ""), req.GetString("check_id", ""))
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

// registerRegenerateAPIKeyTool registers regenerate_api_key, the proxy for
// POST /api/v1/api-keys/{id}/regenerate ("same key, new secret"). It was
// left out of MCP at first on the grounds
// that create_api_key plus revoke_api_key compose it, but they do not: revoke
// CASCADES to every key the old one minted, while regenerate deletes only the
// old key and keeps its name, scope, monitor binding and expiry policy. An
// agent rotating a leaked key had no way to do that without taking every
// descendant key down with it.
func registerRegenerateAPIKeyTool(s *server.MCPServer) {
	s.AddTool(
		newTool("regenerate_api_key",
			mcp.WithDescription("Replace an API key's secret: a new key with the same name, scope and, for a tracing key, the same "+
				"monitor is created, and THE OLD KEY STOPS WORKING IMMEDIATELY, in every job, exporter, dotfile and agent that still "+
				"holds it. If it is the key you are calling with, your next call fails until you switch to the new one. The new key "+
				"has a NEW id. A key that never expired still never expires; one that had an expiry gets a fresh 90 days, capped at "+
				"your own key's expiry. Unlike revoke_api_key this does not cascade: keys the old key created keep working. Refused "+
				"(403, with max_scope) for a key with a higher scope than yours. The plaintext key is returned ONCE and cannot be "+
				"retrieved again: write it where the old one was used, and never echo it back to the person."),
			mcp.WithString("api_key_id", mcp.Required(),
				mcp.Description("UUID of the key to regenerate. Get it from list_api_keys.")),
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
			return c.regenerateAPIKey(ctx, id)
		},
	)
}

// regenerateAPIKey issues POST /api/v1/api-keys/{id}/regenerate. It has no
// body and no retry: the server derives the new key's scope and expiry from
// the old key and the caller, so there is nothing for the tool to choose.
func (c *APIClient) regenerateAPIKey(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/api-keys/"+url.PathEscape(id)+"/regenerate", nil)
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
	if resp.StatusCode != http.StatusCreated {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	// The server answers 201 whether or not deleting the original key
	// succeeded (the replacement works either way) and says which in
	// old_key_revoked, so the answer claims the old key is dead only when
	// the server says it is.
	var created struct {
		createdKey
		OldKeyRevoked *bool `json:"old_key_revoked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	bound := ""
	if created.CheckID != "" {
		bound = ", monitor=" + created.CheckID
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"API key regenerated (new id=%s, name=%q, prefix=%s, scope=%s%s, expires_at=%s). %s\n\n"+
			"PLAINTEXT KEY (shown ONCE, it cannot be retrieved again):\n%s",
		created.ID, created.Name, created.Prefix, created.Scope, bound, expiresOrNever(created.ExpiresAt),
		oldKeySentence(id, created.OldKeyRevoked), created.Key)), nil
}

// oldKeySentence words what happened to the regenerated key's original from
// the server's old_key_revoked: definite when the server reports it either
// way, hedged when a server that predates the field leaves it out.
func oldKeySentence(id string, revoked *bool) string {
	switch {
	case revoked == nil:
		return fmt.Sprintf("The old key %s is revoked; if list_api_keys still shows it, revoke it with revoke_api_key.", id)
	case *revoked:
		return fmt.Sprintf("The old key %s no longer works.", id)
	default:
		return fmt.Sprintf("The old key %s was NOT revoked (the server could not delete it) and still works: revoke it with revoke_api_key.", id)
	}
}

// parentExpiryCapDetail is the EXACT problem detail POST /api/v1/api-keys
// answers with when the requested expires_at is later than the expiry of the
// key making the request. The published OpenAPI document states the same
// sentence, and the hosted server at mcp.lastping.dev matches on it the same
// way.
const parentExpiryCapDetail = "expires_at may not exceed the creating key's own expiry"

// createAPIKey issues POST /api/v1/api-keys. expiresAt is RFC 3339, or an
// empty string to default to a 90-day expiry (mcpDefaultKeyExpiry) — MCP
// never sends an actually-omitted expires_at on the first attempt, unlike
// REST's bare "omitted means never".
//
// That default is not always acceptable to the server, and the case is the
// COMMON one rather than an edge: a key minted by a key can never outlive it,
// and the calling key almost always has an expiry of its own (console keys,
// connect-page keys and keys minted by this tool all default to 90 days). So
// "now + 90 days" is later than the parent's own expiry from the parent's
// first second, and sending it unconditionally would refuse essentially every
// agent's create_api_key call.
//
// The recovery is to retry ONCE using the ceiling the refusal hands back. That
// 400 carries max_expires_at — the latest expiry this caller could have asked
// for — so the retry asks for exactly that, which is by definition no later
// than the 90 days just refused and so never lengthens a key's life. Asking
// for the ceiling explicitly, rather than omitting expires_at and letting the
// server decide, is what makes this work against a server where inheritance is
// switched off: an omitted value there means
// "never", which is not what the agent should get from a key that expires in
// an hour. Omitting is kept only as the fallback for a refusal that carries no
// ceiling.
//
// It is deliberately narrow: only on that one exact detail, and only when the
// agent did not name an expiry itself. An explicit expires_at the server
// refuses is surfaced as an error, because silently substituting a different
// lifetime for the one the agent asked for would be the tool editing its
// caller's intent.
func (c *APIClient) createAPIKey(ctx context.Context, name, expiresAt, scope, checkID string) (*mcp.CallToolResult, error) {
	payload := map[string]any{"name": name}
	// An empty scope asks the server to apply ITS OWN default (currently
	// "write") rather than sending one from here, which would put that
	// policy in two places. The same for check_id: absent means unbound.
	if scope != "" {
		payload["scope"] = scope
	}
	if checkID != "" {
		payload["check_id"] = checkID
	}
	created, errResult := c.mintKey(ctx, "/api/v1/api-keys", payload, expiresAt)
	if errResult != nil {
		return errResult, nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"API key created (id=%s, name=%q, prefix=%s, scope=%s).\n\n"+
			"PLAINTEXT KEY (shown ONCE — store it now, it cannot be retrieved again):\n%s",
		created.ID, created.Name, created.Prefix, created.Scope, created.Key)), nil
}

// createdKey is the one-time response of both minting routes.
type createdKey struct {
	APIKey
	Key string `json:"key"`
}

// mintKey POSTs payload to path (POST /api/v1/api-keys or
// POST /api/v1/checks/{id}/ingest-keys) with the expiry rule described on
// createAPIKey above: an omitted expiresAt is sent as now + 90 days, and the
// ONE refusal that carries a ceiling (the parent-expiry cap) is retried once
// at that ceiling. Both minting tools share it, so an agent's key never gets
// a longer life by going through the other one.
func (c *APIClient) mintKey(ctx context.Context, path string, payload map[string]any, expiresAt string) (*createdKey, *mcp.CallToolResult) {
	agentSupplied := expiresAt != ""
	if !agentSupplied {
		expiresAt = time.Now().UTC().Add(mcpDefaultKeyExpiry).Format(time.RFC3339)
	}

	resp, errResult := c.postKey(ctx, path, payload, expiresAt)
	if errResult != nil {
		return nil, errResult
	}

	if resp.StatusCode == http.StatusBadRequest && !agentSupplied {
		// problemDetail consumes and closes the body, so this is the one and
		// only read of that response.
		info, perr := c.problemDetail(resp)
		if info.Detail != parentExpiryCapDetail {
			// Every other 400 — the scope cap included — is surfaced as it
			// came back. The scope cap is deliberately NOT retried at a lower
			// tier: substituting a scope the agent did not ask for would be
			// the tool editing its caller's intent, the same rule an explicit
			// expires_at already follows.
			return nil, mcp.NewToolResultError(perr.Error())
		}
		// max_expires_at is the ceiling. Omitting expires_at is the fallback
		// for a server that did not send one, and it is strictly worse: on a
		// server with inheritance disabled an omitted value means "never".
		resp, errResult = c.postKey(ctx, path, payload, info.MaxExpiresAt)
		if errResult != nil {
			return nil, errResult
		}
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, mcp.NewToolResultError(c.problem(resp).Error())
	}
	defer resp.Body.Close()

	var created createdKey
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err))
	}
	return &created, nil
}

// postKey sends one minting POST and hands back the raw response so the
// caller can branch on its status. An empty expiresAt omits the field
// entirely rather than sending an empty string, which is what asks the server
// to apply its own rule for an absent value. payload is not modified.
//
// The returned response's body is still open and belongs to the caller. A
// non-nil second return means the request never reached the server, and there
// is no response to close.
func (c *APIClient) postKey(ctx context.Context, path string, payload map[string]any, expiresAt string) (*http.Response, *mcp.CallToolResult) {
	body := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		body[k] = v
	}
	if expiresAt != "" {
		body["expires_at"] = expiresAt
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("failed to encode request: %v", err))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err))
	}
	return resp, nil
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
