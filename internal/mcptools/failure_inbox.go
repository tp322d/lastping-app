package mcptools

// failure_inbox.go — the two tools that close the failure-delivery loop over
// MCP: list_open_incidents (a proxy to GET /api/v1/agents/{id}/open-incidents)
// and add_incident_note (a proxy to POST /api/v1/incidents/{id}/notes).
//
// The loop is: an agent reads why its last run failed, then writes back a
// diagnosis a human can read. Both endpoints shipped, deployed and documented
// before these tools existed, so this file adds NO capability — it makes an
// existing one reachable from the surface an agent actually has. Until now the
// only way to exercise the loop end to end over MCP was to mint an API key
// with create_api_key and curl the endpoints by hand, which is the tell that
// half the loop was unreachable.
//
// WHY BOTH TOOLS LIVE IN ONE FILE: they are two halves of one instruction.
// list_open_incidents without add_incident_note is a report an agent reads and
// nobody hears about; add_incident_note without the inbox is a note about an
// incident the agent had no way to learn of. Splitting them would invite an
// edit that improves one description and leaves the other pointing at a loop
// that no longer matches.
//
// THIS FILE DOES NO VALIDATION OF ITS OWN, for the same reason
// run_expectations.go does not: the API is the single source of truth for what
// a valid note is (empty, whitespace-only and over-8 KB bodies are all
// rejected server-side, with a distinct problem code each), and duplicating
// those rules here would produce a second, drifting answer. It also does not
// reason about the inbox payload — the enrichments are forwarded verbatim as
// raw JSON rather than decoded into a struct, so a field the API adds later
// reaches the agent without a change here, and a field it already returns
// cannot be silently dropped by a narrow local type.
//
// That last property also keeps this file a thin client: it evaluates no
// assertions, builds no failure summaries and writes no prompts, and nothing
// here needs to. It shuttles JSON to and from two HTTP endpoints.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultOpenIncidentLimit matches the API's own default page size. It is
// sent explicitly rather than omitted so the tool's stated
// default cannot drift from the one the agent actually gets.
const defaultOpenIncidentLimit = 50

// listOpenIncidentsDesc is what an agent reads before deciding whether to
// call. Its job is to answer one question: what does this payload tell me
// that I could not work out from my own run? "Your check failed" is worth
// nothing to the agent that just failed.
const listOpenIncidentsDesc = "Returns an agent's failure inbox: every open incident on the monitors it owns, newest first, with context no single failure body carries. " +
	"For learning, at a run's start, what broke meanwhile. " +
	"failure_signature.occurrences: how often this exact failure has been seen (first_seen, last_seen, fingerprint), which separates retry from escalate. " +
	"failed_step: the last step reported (for 'stalled', where the live run is stuck). " +
	"exit_code: 137 (usually OOM) and 1 differ. " +
	"duration_vs_normal: e.g. '8.2x the typical run (41m vs 5m), from 30 archived days', plus run_ms, typical_ms, ratio, days_sampled " +
	"(0: the norm is the median of 5+ recent runs). " +
	"cause: 'fail' (the job reported an error), 'silence' (it never reported; usually the scheduler or host) or 'upstream' (consecutive model-provider API errors; detail names the last). " +
	"Also body_excerpt, run_id, ci.run_url. A missing field is no evidence, not none, normal or a clean exit; exit_code 0 marks a success that failed its expectations. " +
	"add_incident_note takes an entry's incident_id. " +
	untrustedDescSentence

// addIncidentNoteDesc states the two properties the feature rests on as
// facts: notes are append-only (a correction is another note), and a note
// records an unfixed failure as well as a fixed one.
const addIncidentNoteDesc = "Adds a diagnosis note to an incident; it appears on the incident's page in order, stored with author 'agent' (no author argument exists). " +
	"Notes are append-only: no edit or delete exists, so a correction is a new note and the history stays evidence. " +
	"Several notes per incident are normal, up to a cap of 50, and a closed incident still accepts them. " +
	"A note can record a failure that was not fixed as well as one that was. Without a note, the incident page shows only the alert."

// addIncidentNoteBodyDesc describes the one field the request carries,
// including the two caps that are otherwise unanticipated 400s.
const addIncidentNoteBodyDesc = "The diagnosis in plain words: what failed, whether it matches earlier failures (failure_signature.occurrences " +
	"from list_open_incidents) and what was done, e.g. 'failed because the upstream API returned 503; same as the last three nights; retried twice'. " +
	"Non-empty, not whitespace-only, at most 8192 bytes; an oversized body is rejected, never truncated. " +
	"The full failure output already lives on the run."

// registerFailureInboxTools registers list_open_incidents and
// add_incident_note.
func registerFailureInboxTools(s *server.MCPServer) {
	s.AddTool(
		newTool("list_open_incidents",
			mcp.WithDescription(listOpenIncidentsDesc),
			mcp.WithString("agent_id", mcp.Required(),
				mcp.Description("Agent UUID (from register_agent or list_agents). The inbox covers every monitor this agent owns.")),
			mcp.WithNumber("limit",
				mcp.Description("Max incidents to return (default 50, max 200). Newest first, so a small limit drops the oldest open incidents, not the newest.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			agentID, err := req.RequireString("agent_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := defaultOpenIncidentLimit
			if v, ok := req.GetArguments()["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			return c.listOpenIncidents(ctx, agentID, limit)
		},
	)

	s.AddTool(
		newTool("add_incident_note",
			mcp.WithDescription(addIncidentNoteDesc),
			mcp.WithNumber("incident_id", mcp.Required(),
				mcp.Description("The incident's numeric id, an entry's incident_id in list_open_incidents. An integer, not a UUID.")),
			mcp.WithString("body", mcp.Required(), mcp.Description(addIncidentNoteBodyDesc)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// RequireInt accepts the JSON number an agent copies straight out
			// of an inbox entry AND the string form some clients send for a
			// numeric argument; both are the same incident id.
			incidentID, err := req.RequireInt("incident_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			body, err := req.RequireString("body")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.addIncidentNote(ctx, incidentID, body)
		},
	)
}

// listOpenIncidents proxies GET /api/v1/agents/{id}/open-incidents?limit=N.
//
// The response is forwarded as raw JSON, deliberately. Decoding into a struct
// the way listIncidents does would drop every enrichment the moment the API
// grew one, and the enrichments ARE the payload here: an inbox entry stripped
// of failure_signature and duration_vs_normal is the "your check failed" the
// agent already knew.
func (c *APIClient) listOpenIncidents(ctx context.Context, agentID string, limit int) (*mcp.CallToolResult, error) {
	url := fmt.Sprintf("%s/api/v1/agents/%s/open-incidents?limit=%d", c.BaseURL, agentID, limit)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		// The API resolves the agent WITHIN the caller's project and answers
		// 404 for an agent that belongs to another one, so this message must
		// not claim the id does not exist anywhere.
		return mcp.NewToolResultError(fmt.Sprintf(
			"Agent not found in this project: agent_id=%s. Use list_agents to find valid IDs, or register_agent to create one.", agentID)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var incidents []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&incidents); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(incidents) == 0 {
		// Says only what the endpoint checked. "No failures" would be a
		// claim about history this call never looked at.
		return mcp.NewToolResultText(fmt.Sprintf(
			"No open incidents for agent %s — nothing on its monitors is broken right now. "+
				"This is the OPEN inbox only; it says nothing about incidents that have already closed. "+
				"For a monitor's history, use list_incidents.", agentID)), nil
	}

	// body_excerpt, detail, failed_step.name and the CI-provider-supplied
	// ci.failing_stage / ci.branch / ci.commit_sha are the fields an
	// incident's own ping, or the CI job behind it, supplies verbatim.
	//
	// run_id belongs with them for the same reason get_run_history's rid does:
	// it is the ?rid= query value off the ping URL, chosen by whoever holds
	// that URL, not an identifier LastPing issues. Looking like an id is not
	// the same as being one.
	//
	// ci.run_url and ci.outcome are excluded: a provider-generated URL and a
	// fixed outcome enum are not outsider-writable text. title does not exist
	// on this payload; it belongs to get_run_history's runs instead.
	//
	// This list is byte-for-byte the hosted server's, and must stay that way.
	return untrustedResult(incidents, "body_excerpt", "detail", "failed_step.name",
		"ci.failing_stage", "ci.branch", "ci.commit_sha", "run_id")
}

// addIncidentNote proxies POST /api/v1/incidents/{id}/notes.
//
// The body reaches the API exactly as the agent wrote it — no trimming, no
// truncation, no prefix. Trimming is the API's decision (it rejects a
// whitespace-only note rather than storing one), and a tool that silently
// reshaped a diagnosis would be editing evidence.
//
// The request carries the body and nothing else: no author, because
// authorship follows the surface a note arrived on and is not the caller's to
// declare, and no created_at, because a note's
// position in the incident's log must not be back-datable by its writer.
func (c *APIClient) addIncidentNote(ctx context.Context, incidentID int, body string) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode note: %v", err)), nil
	}

	url := fmt.Sprintf("%s/api/v1/incidents/%d/notes", c.BaseURL, incidentID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to build request: %v", err)), nil
	}
	c.auth(httpReq)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("API request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		// One status, two readings, and the API refuses to distinguish them on
		// purpose: an incident in another project answers exactly like one
		// that was never created, so this endpoint cannot be used to probe for
		// other tenants' incident ids. The message keeps both readings open
		// rather than asserting the id does not exist.
		return mcp.NewToolResultError(fmt.Sprintf(
			"Incident %d not found in this project — either no incident has that id, or it belongs to another project; "+
				"the API answers the same way for both and will not say which. Take incident_id from list_open_incidents.", incidentID)), nil
	case http.StatusConflict:
		// The ONLY conflict here, and it is not declare-once: a second note is
		// ordinary. 409 means this incident has hit its 50-note cap.
		return mcp.NewToolResultError(fmt.Sprintf(
			"Cannot add a note to incident %d: %v. Notes are append-only and this incident has reached its cap of 50; "+
				"something is looping rather than diagnosing. Alert a human instead.", incidentID, c.problem(resp))), nil
	case http.StatusCreated:
		// falls through to the decode below
	default:
		// Every other rejection — an empty or whitespace-only body, an
		// oversized one, a malformed id — reaches the agent verbatim. An agent
		// told "ok" when its note was refused does not retry, and the
		// diagnosis is lost silently.
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var note struct {
		ID         int64  `json:"id"`
		IncidentID int64  `json:"incident_id"`
		Author     string `json:"author"`
		Body       string `json:"body"`
		CreatedAt  string `json:"created_at"`
	}
	if dErr := json.NewDecoder(resp.Body).Decode(&note); dErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", dErr)), nil
	}

	out, mErr := json.MarshalIndent(note, "", "  ")
	if mErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to render response: %v", mErr)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Note added to incident %d. Notes are append-only: to correct this, add another note — it cannot be edited or deleted.\n%s",
		note.IncidentID, string(out))), nil
}
