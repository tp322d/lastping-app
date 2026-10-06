package mcptools

// agents.go — MCP tools for the agent registry (/api/v1/agents).
//
// register_agent is the headline capability of this phase: it lets an
// autonomous agent create its own registry entry and, in the same
// conversation, learn how to attach a monitor to itself (create_monitor with
// agent_id) and instrument that monitor's pings (get_ping_instructions) —
// nothing here requires a human to visit the dashboard first.
//
// ATTACHMENT RULE: monitors attach to an agent by passing the agent's id or
// slug (as returned here) to create_monitor/update_monitor's agent_id
// parameter. Naming an agent that does not exist is a 400 UNKNOWN_AGENT — it
// is NEVER an implicit create. That rule is enforced server-side and is
// repeated in every tool description below so an agent cannot miss it.

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

// Agent mirrors the LastPing Agent resource returned by list_agents and
// get_agent. Status is never stored — it is rolled up live from the
// monitors the agent owns, so this struct just decodes whatever the API
// already computed.
type Agent struct {
	ID           string  `json:"id"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Status       string  `json:"status"`
	MonitorCount int64   `json:"monitor_count"`
	LastSeen     *string `json:"last_seen,omitempty"`
	CreatedAt    string  `json:"created_at"`
	// Usage24h is the agent's model usage over the last 24 hours, summed
	// over every model (so its model and provider are empty; null when it
	// made no model call), and TopDependencies its five heaviest outgoing
	// dependencies over the same window, both from its OpenTelemetry traces.
	// They are read-only facts the list and detail routes compute; omitempty
	// only so update_agent, which confirms a rename, can leave them out (see
	// updateAgent).
	Usage24h        *UsageDay    `json:"usage_24h,omitempty"`
	TopDependencies []Dependency `json:"top_dependencies,omitempty"`
}

// agentUntrustedFields names the Agent fields an exporter can have written:
// a dependency's name and a model or provider come from span attributes, and
// name and slug are the trace source's service.name verbatim when the agent
// was created by adopting a discovered source.
var agentUntrustedFields = []string{"name", "slug", "top_dependencies.name", "usage_24h.model", "usage_24h.provider"}

// AgentRegistration is the structured output of register_agent: the new
// agent's identity plus NextSteps, which tells it exactly how to get a
// working monitor.
type AgentRegistration struct {
	AgentID     string `json:"agent_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	NextSteps   string `json:"next_steps"`
}

func registerAgentTools(s *server.MCPServer) {
	// register_agent
	s.AddTool(
		newTool("register_agent",
			mcp.WithDescription("Registers an autonomous agent in the project's agent registry and returns its id, slug and wire-up steps. "+
				"One agent stands for one autonomous worker, which can own many monitors: create_monitor and update_monitor attach a monitor through agent_id (the id or the slug). "+
				"An agent_id that does not exist is an error (400 UNKNOWN_AGENT), never an implicit create. "+
				"The slug is derived from the name, so registering the same name again is refused (409) rather than creating a second agent."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable agent name, e.g. 'Deploy Bot'. Used to derive the agent's slug.")),
			mcp.WithString("description", mcp.Description("Optional free-text description of what this agent does. Omitted: none.")),
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
			return c.registerAgent(ctx, name, req.GetString("description", ""))
		},
	)

	// list_agents
	s.AddTool(
		newTool("list_agents",
			mcp.WithDescription("Lists the project's agents: id, slug, name, status, monitor_count, last_seen. "+
				"status is rolled up live from the agent's monitors, worst first: down, blocked (a run needs a human), late, running, up, "+
				"pending (no report yet) or idle (no monitors, or all paused or in maintenance). "+
				"usage_24h: model tokens and cost over 24 hours (null with no model call); top_dependencies: its five heaviest outgoing dependencies. "+
				untrustedDescSentence),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listAgents(ctx)
		},
	)

	// get_agent
	s.AddTool(
		newTool("get_agent",
			mcp.WithDescription("Gets one agent by UUID, with the same fields as list_agents: its live status rollup, usage_24h and top_dependencies. "+
				untrustedDescSentence),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents).")),
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
			return c.getAgent(ctx, id)
		},
	)

	// update_agent
	s.AddTool(
		newTool("update_agent",
			mcp.WithDescription("Updates an agent's name, description or slug by UUID with merge-patch semantics: supplied fields change, omitted ones keep their value. "+
				"Renaming never changes the slug. A new slug has to be unique in the project; saved links, Terraform references and trace sources (service.name) "+
				"naming the old slug then stop matching the agent unless they equal its name (case-insensitive), past traced runs included, "+
				"and a slug equal to a source another agent receives takes that source's traces, past runs included. Attached monitors stay attached."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents).")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable agent name, e.g. 'Deploy Bot'.")),
			mcp.WithString("description", mcp.Description("Free-text description of what this agent does. Omitted: unchanged. "+
				"An explicit empty string clears it.")),
			mcp.WithString("slug", mcp.Description("New slug for the agent, e.g. 'reddit-bot': 3-50 characters, lowercase letters, "+
				"digits and hyphens. Omitted: unchanged. After a change, references to the old slug "+
				"(links, Terraform, trace sources and their past runs) stop matching this agent unless they equal its name (case-insensitive).")),
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
			return c.updateAgent(ctx, id, req)
		},
	)

	// delete_agent
	s.AddTool(
		newTool("delete_agent",
			mcp.WithDescription("Permanently deletes an agent from the registry by UUID; cannot be undone. Its monitors are not deleted: "+
				"each survives with its ping history and incidents, becomes unowned (agent_id null) and keeps running. "+
				"update_monitor can attach a survivor to another agent, and delete_monitor removes a monitor."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents).")),
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
			return c.deleteAgent(ctx, id)
		},
	)
}

// --- APIClient methods for agents ---

func (c *APIClient) registerAgent(ctx context.Context, name, description string) (*mcp.CallToolResult, error) {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/agents", bytes.NewReader(data))
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

	var ag Agent
	if err := json.NewDecoder(resp.Body).Decode(&ag); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	reg := AgentRegistration{
		AgentID:     ag.ID,
		Slug:        ag.Slug,
		Name:        ag.Name,
		Description: ag.Description,
		NextSteps: fmt.Sprintf("Agent registered. To attach a monitor to it, call create_monitor with agent_id=%q (or agent_id=%q — either the id or the slug works). "+
			"Naming an agent that does not exist is an error, never an implicit create, so keep this agent_id for every monitor this agent owns. "+
			"Once the monitor exists, call get_ping_instructions with its id and read its reporting_options field to choose how this agent should report: "+
			"how_to, the manual protocol, is the UNIVERSAL default — it works in any agent, any language, any tool, with no prerequisite, so set "+
			"expect_every_s (the silence floor) alongside it and a lapse opens a detected incident instead of the monitor reading healthy forever. "+
			"get_ping_instructions returns hook_install, an optional shortcut that automates the same protocol through the named client's own hooks and can additionally send blocked/note, "+
			"when its tool argument is \"claude-code\" (or \"codex\" or \"antigravity\"); without tool it returns no hook_install. "+
			"A hook install is specific to the client it names: its steps do not carry over to a different agent, which reports through how_to.",
			ag.Slug, ag.ID),
	}

	out, _ := json.MarshalIndent(reg, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func (c *APIClient) listAgents(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/agents", nil)
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

	var agents []Agent
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(agents) == 0 {
		return mcp.NewToolResultText("No agents found. Create one with register_agent."), nil
	}

	return untrustedResult(agents, agentUntrustedFields...)
}

func (c *APIClient) getAgent(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/agents/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Agent not found: id=%s. Use list_agents to find valid IDs, or register_agent first.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ag Agent
	if err := json.NewDecoder(resp.Body).Decode(&ag); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	return untrustedResult(ag, agentUntrustedFields...)
}

func (c *APIClient) updateAgent(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	// PATCH /api/v1/agents/{id} is RFC 7396 merge-patch: an absent key
	// preserves the stored value, an explicit "" clears description to empty
	// (the column is NOT NULL DEFAULT ''). The distinction that matters is
	// PRESENCE in the arguments map, not truthiness of the value — checking
	// `ok` here (not `v != ""`) is what lets a caller send an empty string to
	// clear, and lets an omitted key fall through untouched so a name-only
	// update_agent call can never silently wipe description.
	if v, ok := args["description"].(string); ok {
		body["description"] = v
	}
	// slug is sent only when the caller passed a non-empty one: an absent slug
	// keeps the stored slug on the REST side, which is what makes a name-only
	// rename unable to change it. An empty string means "not asked" here (a
	// slug cannot be empty), never a request to clear it.
	if v, ok := args["slug"].(string); ok && strings.TrimSpace(v) != "" {
		body["slug"] = v
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/agents/"+id, bytes.NewReader(data))
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
		return mcp.NewToolResultError(fmt.Sprintf("Agent not found: id=%s. Use list_agents to find valid IDs, or register_agent first.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ag Agent
	if err := json.NewDecoder(resp.Body).Decode(&ag); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}
	// update_agent confirms a rename; it is not a read, so the two trace
	// facts stay out (get_agent carries them). What it does return goes in
	// the untrusted-output envelope, like get_agent's: name and slug are a
	// trace source's service.name verbatim when the agent was adopted from a
	// discovered source, which is exporter text, not LastPing's.
	ag.Usage24h, ag.TopDependencies = nil, nil
	return untrustedResult(ag, "name", "slug")
}

func (c *APIClient) deleteAgent(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/agents/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Agent not found: id=%s. Use list_agents to find valid IDs, or register_agent first.", id)), nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Agent %s deleted. Its monitors were NOT deleted: they survive with agent_id cleared (unowned), keeping their ping history "+
			"and incidents, and continue running on their existing schedule. Reattach one with update_monitor's agent_id, or remove "+
			"it entirely with delete_monitor.", id)), nil
}
