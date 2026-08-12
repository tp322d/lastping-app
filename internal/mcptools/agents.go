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
}

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
		mcp.NewTool("register_agent",
			mcp.WithDescription("Register a new autonomous agent in the project's agent registry, returning its id, slug and wire-up "+
				"instructions in one call — so an agent can go from nothing to reporting in a single conversation. "+
				"Call this ONCE per autonomous worker, not once per monitor. "+
				"ATTACHMENT RULE: after registering, attach monitors to this agent by passing the returned agent_id (its id OR its slug) "+
				"to create_monitor's agent_id parameter. Naming an agent that does not exist is an error (400 UNKNOWN_AGENT) — "+
				"it is NEVER an implicit create, so re-running this tool with the same name is the only way to get a new agent_id to attach to. "+
				"Re-registering with the same name is safe: the API derives a stable slug from name and rejects a duplicate slug rather than creating a second row."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable agent name, e.g. 'Deploy Bot'. Used to derive the agent's slug.")),
			mcp.WithString("description", mcp.Description("Optional free-text description of what this agent does. Omit for none.")),
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
		mcp.NewTool("list_agents",
			mcp.WithDescription("List all agents registered in the project. Returns id, slug, name, status, monitor_count and last_seen "+
				"for each. status is rolled up live from the monitors the agent owns, worst first: down (a monitor is down), "+
				"blocked (a monitor's run needs a human right now), late (a monitor is late), running (a monitor's run is in "+
				"flight), up (healthy), pending (a monitor exists but has never reported) or idle (no monitors, or all of them "+
				"paused/in maintenance). Use register_agent to create one.")),
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
		mcp.NewTool("get_agent",
			mcp.WithDescription("Get a single LastPing agent by UUID. Returns the same fields as list_agents, including its live "+
				"status rollup. Use list_agents to find valid IDs, or register_agent to create one."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents)."))),
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
		mcp.NewTool("update_agent",
			mcp.WithDescription("Update an existing LastPing agent's name/description by UUID using merge-patch semantics: only the "+
				"fields you supply are changed, and any field you omit keeps its current stored value. slug is derived from name at "+
				"creation and is immutable — this can rename the agent's display name, but never its slug, so anything that already "+
				"references it by slug (including monitors attached via agent_id) keeps working."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents).")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable agent name, e.g. 'Deploy Bot'.")),
			mcp.WithString("description", mcp.Description("Free-text description of what this agent does. Omit to leave the agent's "+
				"current description unchanged — THIS IS THE DEFAULT AND SAFE CHOICE for a name-only rename. Pass an explicit empty "+
				"string to clear an existing description back to none.")),
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
		mcp.NewTool("delete_agent",
			mcp.WithDescription("Permanently delete a LastPing agent from the registry by UUID. THIS DOES NOT DELETE ITS MONITORS: "+
				"the agent_id foreign key on a monitor is ON DELETE SET NULL, so every monitor this agent owned survives the delete "+
				"with its ping history and incidents completely intact — it just becomes unowned (agent_id cleared to null) and keeps "+
				"running on its existing schedule, no longer attributed to any agent. list_monitors/get_monitor will still show it "+
				"afterwards. To reattach a survivor, call update_monitor with agent_id set to a different agent's id or slug. To also "+
				"remove a monitor, call delete_monitor on it separately — deleting the agent alone never does that. This action on the "+
				"agent row itself cannot be undone."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Agent UUID (from register_agent or list_agents)."))),
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
			"expect_every_s (the silence floor, set via update_monitor) alongside it and a lapse opens a detected incident instead of the monitor reading healthy forever. "+
			"If this agent IS Claude Code specifically, hook_install is available as an optional shortcut that automates the exact same protocol via "+
			"Claude Code's own hooks and can additionally send blocked/note; a different agent, even one with its own hook system, must NOT translate "+
			"hook_install's steps — they are Claude Code specific and a translated install verifies clean while never reporting, so use how_to instead.",
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

	out, _ := json.MarshalIndent(agents, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
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

	out, _ := json.MarshalIndent(ag, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
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

	out, _ := json.MarshalIndent(ag, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("Agent updated:\n%s", out)), nil
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
