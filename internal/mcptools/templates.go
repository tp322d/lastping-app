package mcptools

// templates.go — MCP tools for per-monitor alert message templates
// (/api/v1/checks/{id}/templates).
//
// event_type and template contents are NOT validated locally: the API is the
// authoritative validator (allowed event types, allowed template variables,
// template length), so a malformed value simply comes back as a normal API
// error. eventTypes below is restated as a literal list rather than derived
// from an internal package, for the same reason.

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

// eventTypes is the canonical list of alert event types, restated here as a
// literal so this package draws no dependency on where the API defines it.
var eventTypes = []string{"down", "recovery", "fail", "every-run", "success", "started", "blocked", "note"}

// eventTypeList returns eventTypes single-quoted and comma-joined (e.g.
// "'down', 'recovery', 'fail', ..."), for use in tool descriptions and error
// messages below.
func eventTypeList() string {
	quoted := make([]string, len(eventTypes))
	for i, t := range eventTypes {
		quoted[i] = "'" + t + "'"
	}
	return strings.Join(quoted, ", ")
}

func registerTemplateTools(s *server.MCPServer) {
	// get_alert_templates
	s.AddTool(
		mcp.NewTool("get_alert_templates",
			mcp.WithDescription("Get all custom alert message templates for a LastPing monitor. "+
				"Returns a map of event-type (or event-type/cause) keys to template strings. "+
				"Keys: "+eventTypeList()+", or 'event_type/cause' "+
				"(e.g. 'down/silence'). "+
				"An empty result means all alerts use the built-in plain-language defaults."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
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
			return c.getAlertTemplatesTool(ctx, id)
		},
	)

	// set_alert_template
	s.AddTool(
		mcp.NewTool("set_alert_template",
			mcp.WithDescription("Set or clear a single alert message template on a monitor. "+
				"The template is validated for allowed variables before saving. "+
				"Pass an empty string for template to reset that entry to the built-in default. "+
				"All other existing templates are preserved (read-modify-write). "+
				"Available variables: {check_name}, {event}, {status}, {cause}, {last_ping}, {schedule}, "+
				"{incident_url}, {run_url}, {branch}, {commit}, {actor}, {failing_stage}, "+
				"{duration}, {latency}, {status_code}, {url}, {last_step}, {step_count}, "+
				"{run_duration}, {body}, {detail}. "+
				"{failing_stage} is CI-only and provider-dependent: always populated on GitLab; on "+
				"GitHub only if the repository webhook also subscribes to the workflow_job event; "+
				"never on Jenkins, whose Notification Plugin payload carries no step detail. "+
				"{body} is the triggering ping's own text (pings.body_excerpt) — it is how a 'blocked' "+
				"or 'note' event's reason reaches the alert, and a custom template is the only way to "+
				"control where in the message it appears."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Monitor UUID.")),
			mcp.WithString("event_type", mcp.Required(), mcp.Description(
				"Event type: "+eventTypeList()+".")),
			mcp.WithString("cause", mcp.Description(
				"Optional cause for a per-cause override (e.g. 'silence', 'overrun', 'never_started', 'stalled', 'runaway'). "+
					"Omit or leave empty for an event-type-wide template.")),
			mcp.WithString("template", mcp.Required(), mcp.Description(
				"Template text with {variable} placeholders. Empty string resets to the built-in default.")),
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
			eventType, err := req.RequireString("event_type")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			args := req.GetArguments()
			cause, _ := args["cause"].(string)
			tmpl, _ := args["template"].(string)
			return c.setAlertTemplateTool(ctx, id, eventType, cause, tmpl)
		},
	)
}

// --- APIClient methods for templates ---

// getAlertTemplatesTool calls GET /api/v1/checks/{id}/templates.
func (c *APIClient) getAlertTemplatesTool(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/templates", nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var payload struct {
		Templates map[string]string `json:"templates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(payload.Templates) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Monitor %s has no custom alert templates — all events use built-in plain-language defaults.", id)), nil
	}

	out, _ := json.MarshalIndent(payload.Templates, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

// setAlertTemplateTool does a read-modify-write to set (or clear) one
// template key. event_type and template content are validated by the API,
// not here.
func (c *APIClient) setAlertTemplateTool(ctx context.Context, id, eventType, cause, tmpl string) (*mcp.CallToolResult, error) {
	// Build the composite key (event_type or event_type/cause).
	cause = strings.TrimSpace(cause)
	key := eventType
	if cause != "" {
		key = eventType + "/" + cause
	}

	// Read current templates.
	current, toolErr := c.fetchTemplates(ctx, id)
	if toolErr != nil {
		return toolErr, nil
	}

	// Modify: set or delete the key.
	resetMode := strings.TrimSpace(tmpl) == ""
	if resetMode {
		delete(current, key)
	} else {
		current[key] = tmpl
	}

	// Write the updated set.
	if toolErr = c.putTemplates(ctx, id, current); toolErr != nil {
		return toolErr, nil
	}

	if resetMode {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Template for %q reset to built-in default on monitor %s.", key, id)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Template for %q set on monitor %s.", key, id)), nil
}

// fetchTemplates GETs the current templates map for a monitor.
// On API error it returns a *mcp.CallToolResult to propagate; on success the result is nil.
func (c *APIClient) fetchTemplates(ctx context.Context, id string) (map[string]string, *mcp.CallToolResult) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/checks/"+id+"/templates", nil)
	if err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("failed to build GET request: %v", err))
	}
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, mcp.NewToolResultError(
			fmt.Sprintf("Monitor not found: id=%s. Use list_monitors to find valid IDs.", id))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mcp.NewToolResultError(c.problem(resp).Error())
	}

	var payload struct {
		Templates map[string]string `json:"templates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, mcp.NewToolResultError(fmt.Sprintf("failed to decode templates response: %v", err))
	}
	if payload.Templates == nil {
		payload.Templates = map[string]string{}
	}
	return payload.Templates, nil
}

// putTemplates PUTs the full templates map to the API.
// Returns a *mcp.CallToolResult on error, nil on success.
func (c *APIClient) putTemplates(ctx context.Context, id string, templates map[string]string) *mcp.CallToolResult {
	body, _ := json.Marshal(map[string]interface{}{"templates": templates})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/v1/checks/"+id+"/templates",
		bytes.NewReader(body))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build PUT request: %v", err))
	}
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return mcp.NewToolResultError(fmt.Sprintf("Monitor not found: id=%s.", id))
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error())
	}
	return nil
}
