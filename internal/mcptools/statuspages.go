package mcptools

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

// StatusPage mirrors the LastPing status page resource. A status page is the
// public/shared face of a set of monitors: the thing a human who is not in
// the project looks at to find out whether the service is up.
type StatusPage struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// CheckIDs is the monitor set shown on the page. It is REPLACE-the-set on
	// every write, which is why update_status_page reads before it writes.
	CheckIDs   []string `json:"check_ids"`
	Visibility string   `json:"visibility"`
	// PublicURL is populated by the API only when visibility is "public"; a
	// private page has no shareable URL, and an empty value here is the honest
	// representation of that rather than a link that would 404.
	PublicURL string `json:"public_url,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Status pages are four new tools rather than an extension of an existing one:
// nothing in this surface previously touched the resource at all, so there was
// no tool to extend. The shape mirrors the CRUD grouping every other resource
// here already uses (create/list/update/delete for monitors, agents,
// destinations), and it deliberately stops at four — the API's
// GET /api/v1/status-pages/{id} returns exactly the fields list already
// returns for every page, so a get_status_page tool would add a fifth entry to
// the tool list that told an agent nothing new.
func registerStatusPageTools(s *server.MCPServer) {
	// statusPageVisibilityDesc is shared by create and update so the one thing
	// an agent could get badly wrong here — publishing monitor names to the
	// open internet — cannot be described two different ways.
	const statusPageVisibilityDesc = "'private' (default) or 'public'. " +
		"'public' means the page is served at a guessable-free but UNAUTHENTICATED URL: anyone with the link sees the title, the name of every monitor on it, " +
		"and its up/down history. Monitor names are frequently internal ('billing-reconciler', 'acme-corp-nightly-sync'), so treat this as publishing them. " +
		"Choose 'private' unless the user has actually asked for a page other people can see. " +
		"The free tier allows exactly ONE public page per project; a second returns 403."

	const statusPageCheckIDsDesc = "Comma-separated monitor UUIDs to show on the page, in no particular order. Get them from list_monitors. " +
		"Every id must belong to this project — an unknown or cross-project id returns 400 and nothing is saved. " +
		"An empty value is legal and produces a page with no monitors on it."

	s.AddTool(
		mcp.NewTool("list_status_pages",
			mcp.WithDescription("List the project's status pages: id, slug, title, the monitors on each, visibility, and the public URL of any public page. "+
				"A status page is how a monitor's health is shown to people who are not in the project — customers, or another team. "+
				"This is also the read you need before update_status_page, because its check_ids REPLACE the page's monitor set."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listStatusPages(ctx)
		},
	)

	s.AddTool(
		mcp.NewTool("create_status_page",
			mcp.WithDescription("Create a status page — a single page showing the current status and recent history of a chosen set of monitors. "+
				"Reach for this when the health of a monitor needs to be visible to someone who cannot log in to the project. "+
				"Pages are PRIVATE unless you ask for otherwise; read the visibility parameter before making one public."),
			mcp.WithString("title", mcp.Required(), mcp.Description("Human-readable page title, e.g. 'Acme API Status'. Shown at the top of the page, and to anyone the page is shared with.")),
			mcp.WithString("check_ids", mcp.Description(statusPageCheckIDsDesc)),
			mcp.WithString("visibility", mcp.Description(statusPageVisibilityDesc)),
			mcp.WithString("slug", mcp.Description("Optional URL slug, which is what appears in the public link (/status/<slug>). "+
				"Must match ^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$ (3-50 chars, lowercase alphanumeric and hyphens, starting and ending alphanumeric). "+
				"Slugs are GLOBALLY unique across all projects, not just yours, so a desirable one may be taken — that returns 409. "+
				"OMIT IT unless the user asked for a specific URL: a random unguessable slug is then generated, which is also the safer default for a public page.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createStatusPage(ctx, req)
		},
	)

	s.AddTool(
		mcp.NewTool("update_status_page",
			mcp.WithDescription("Update a status page's title, slug, visibility, or the set of monitors on it. Only the arguments you pass are changed; "+
				"anything you omit keeps its current value (this tool reads the page first and merges, so omitting check_ids can never blank the page). "+
				"check_ids, when you DO pass it, REPLACES the whole monitor set — to add one monitor, pass the existing ids plus the new one, "+
				"which list_status_pages gives you. Changing the slug changes the public URL and BREAKS any link already shared."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Status page UUID, from list_status_pages.")),
			mcp.WithString("title", mcp.Description("New page title. Omit to leave unchanged.")),
			mcp.WithString("check_ids", mcp.Description(statusPageCheckIDsDesc+
				" REPLACES the page's whole monitor set. Omit to leave the current set alone.")),
			mcp.WithString("visibility", mcp.Description(statusPageVisibilityDesc+
				" Omit to leave unchanged. Switching a page from private to public publishes every monitor name already on it.")),
			mcp.WithString("slug", mcp.Description("New URL slug. Omit to leave unchanged — which is almost always right, because changing it breaks every link "+
				"already handed out. Same format rules and same global uniqueness as on create; a taken slug returns 409.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.updateStatusPage(ctx, id, req)
		},
	)

	s.AddTool(
		mcp.NewTool("delete_status_page",
			mcp.WithDescription("Permanently delete a status page. This cannot be undone, and any public URL it had stops working immediately. "+
				"The monitors on the page are NOT affected — they keep running and alerting exactly as before; only the shared view of them is removed. "+
				"To stop sharing without losing the page, set visibility to 'private' with update_status_page instead."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Status page UUID, from list_status_pages.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.deleteStatusPage(ctx, id)
		},
	)
}

// --- APIClient methods for status pages ---

func (c *APIClient) listStatusPages(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/status-pages", nil)
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

	var pages []StatusPage
	if err := json.NewDecoder(resp.Body).Decode(&pages); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(pages) == 0 {
		return mcp.NewToolResultText("No status pages found. Create one with create_status_page."), nil
	}

	out, _ := json.MarshalIndent(pages, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) createStatusPage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["title"].(string); ok && v != "" {
		body["title"] = v
	}
	if v, ok := args["slug"].(string); ok && v != "" {
		body["slug"] = v
	}
	if v, ok := args["visibility"].(string); ok && v != "" {
		body["visibility"] = v
	}
	// check_ids is always sent, even when empty: the API reads a missing key as
	// "no monitors", which is what an omitted argument means on a create too.
	if v, ok := args["check_ids"].(string); ok {
		body["check_ids"] = splitIDs(v)
	} else {
		body["check_ids"] = []string{}
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/status-pages", bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var sp StatusPage
	if err := json.NewDecoder(resp.Body).Decode(&sp); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(statusPageSummary("Status page created", sp)), nil
}

// updateStatusPage patches a status page with read-modify-write semantics.
//
// PATCH /api/v1/status-pages/{id} is a full replacement despite its verb: it
// REQUIRES slug, title and visibility on every call and overwrites check_ids
// with whatever it is given. Forwarding the tool's arguments straight through
// would therefore mean that renaming a page wiped its monitor set and its
// visibility — the single most likely thing an agent would do, and it would
// look like it succeeded. So the current page is fetched first and the caller's
// arguments are merged over it, which is what lets this tool honestly advertise
// "only the fields you pass are changed" like update_monitor does.
func (c *APIClient) updateStatusPage(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	current, err := c.getStatusPage(ctx, id)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"Could not read status page %s before updating it (%v). Nothing was changed. "+
				"The update is not attempted without the current values, because the API replaces every field on write — "+
				"patching blind would silently drop the page's monitors and its visibility.", id, err)), nil
	}

	args := req.GetArguments()
	// A nil CheckIDs would marshal to JSON null. The API tolerates that, but
	// the normalisation is done here rather than relied upon: this body is a
	// full replacement, and "the field the server had to guess at" is not a
	// thing a replace-everything write should contain.
	currentCheckIDs := current.CheckIDs
	if currentCheckIDs == nil {
		currentCheckIDs = []string{}
	}
	body := map[string]interface{}{
		"slug":       current.Slug,
		"title":      current.Title,
		"visibility": current.Visibility,
		"check_ids":  currentCheckIDs,
	}
	if v, ok := args["title"].(string); ok && v != "" {
		body["title"] = v
	}
	if v, ok := args["slug"].(string); ok && v != "" {
		body["slug"] = v
	}
	if v, ok := args["visibility"].(string); ok && v != "" {
		body["visibility"] = v
	}
	// An explicitly supplied check_ids replaces the set, including with an
	// empty one — "show no monitors" is a real state, and it is only reachable
	// by passing the argument, never by omitting it.
	if v, ok := args["check_ids"].(string); ok {
		body["check_ids"] = splitIDs(v)
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/status-pages/"+id, bytes.NewReader(data))
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
		return mcp.NewToolResultError(fmt.Sprintf("Status page not found: id=%s. Use list_status_pages to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var sp StatusPage
	if err := json.NewDecoder(resp.Body).Decode(&sp); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	return mcp.NewToolResultText(statusPageSummary("Status page updated", sp)), nil
}

// getStatusPage reads one status page. It returns an error rather than a tool
// result because its only caller, updateStatusPage, turns a failure into a
// "nothing was changed" message rather than surfacing it raw.
func (c *APIClient) getStatusPage(ctx context.Context, id string) (StatusPage, error) {
	var sp StatusPage

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/status-pages/"+id, nil)
	if err != nil {
		return sp, fmt.Errorf("failed to build request: %w", err)
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return sp, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return sp, fmt.Errorf("status page not found: id=%s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return sp, c.problem(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&sp); err != nil {
		return sp, fmt.Errorf("failed to decode response: %w", err)
	}
	return sp, nil
}

func (c *APIClient) deleteStatusPage(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/status-pages/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Status page not found: id=%s. Use list_status_pages to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Status page %s deleted. Its monitors are unaffected and still alerting; only the shared view is gone.", id)), nil
}

// statusPageSummary renders the one-line result both write tools return. It
// names the public URL explicitly when there is one, because "the page is
// public" and "here is the link people will use" are different facts and only
// the second is actionable.
func statusPageSummary(verb string, sp StatusPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (id=%s, slug=%s, title=%q, visibility=%s, monitors=%d)",
		verb, sp.ID, sp.Slug, sp.Title, sp.Visibility, len(sp.CheckIDs))
	if sp.PublicURL != "" {
		fmt.Fprintf(&b, "\nPUBLIC — anyone with this link can see it, including the name of every monitor on it: %s", sp.PublicURL)
	} else {
		b.WriteString("\nPrivate: visible only to project members, with no shareable URL. " +
			"Set visibility='public' with update_status_page if it needs one.")
	}
	return b.String()
}
