package mcptools

// failure_inbox.go — the two tools that close the failure-delivery loop over
// MCP: list_open_incidents (a proxy to GET /api/v1/agents/{id}/open-incidents,
// api/agent_inbox.go) and add_incident_note (a proxy to
// POST /api/v1/incidents/{id}/notes, api/incident_notes.go).
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
// That last property is also the import boundary: nothing in this file
// imports core/assertion, core/failprint or internal/agentprompt, and nothing
// here needs to. It shuttles JSON to and from two HTTP endpoints. See
// agentprompt_boundary_test.go, which enforces that mechanically with
// `go list -deps -test`.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultOpenIncidentLimit matches the API's own default (api/events.go's
// parseLimit). It is sent explicitly rather than omitted so the tool's stated
// default cannot drift from the one the agent actually gets.
const defaultOpenIncidentLimit = 50

// listOpenIncidentsDesc is what an agent reads before deciding whether to
// call. Its job is to answer one question: what does this payload tell me
// that I could not work out from my own run? "Your check failed" is worth
// nothing to the agent that just failed.
const listOpenIncidentsDesc = "Read this agent's failure inbox: every incident currently OPEN on the monitors it owns, newest first. " +
	"Call it at the START of a run, before doing the work — this is how an agent finds out what broke while it was not running, " +
	"with no webhook, chat integration or mailbox to wire up. " +
	"What makes the payload worth reading is NOT 'your check failed' — the run that failed already knows that. It is the context " +
	"that no single failure body can contain:\n" +
	"- failure_signature.occurrences — how many times THIS EXACT failure has been seen on this monitor (with first_seen/last_seen, " +
	"and a fingerprint you can use to correlate incidents yourself). First occurrence or fortieth repeat is the fact that decides " +
	"retry versus escalate, and no amount of reasoning over one failure body can recover it.\n" +
	"- failed_step — the last step the run reported before it stopped. For a 'stalled' incident this is the entire diagnosis: the " +
	"run is still alive and has not moved past this step.\n" +
	"- exit_code — the status the run exited with. 137 (SIGKILL, usually the OOM killer) and 1 are both the word 'fail' and are " +
	"completely different problems.\n" +
	"- duration_vs_normal — a COMPARISON, not a measurement: '8.2x the typical run (41m vs 5m), from 30 archived days'. run_ms, " +
	"typical_ms, ratio and days_sampled are carried too, so you can apply your own threshold and tell a 30-day norm from a 2-day one.\n" +
	"- cause — 'silence' and 'fail' demand opposite responses. 'fail' means the job ran and reported an error; 'silence' means it " +
	"never reported at all, which usually implicates the scheduler or the host rather than the job.\n" +
	"- body_excerpt (the error text the failing run actually printed), run_id (line the incident up against your own logs), and " +
	"ci.run_url (where the full log is, when the failure came from a CI provider).\n" +
	"ABSENCE MEANS NO EVIDENCE — NEVER GOOD NEWS. Every enrichment degrades to ABSENT rather than erroring, so a missing field is " +
	"the ordinary case, not an error. A missing duration_vs_normal means the run's duration or the monitor's baseline is unknown; it " +
	"does NOT mean the run took a normal amount of time. A missing exit_code means no numeric code was reported (the ping used a word " +
	"form such as /fail, or a detector opened the incident with no ping at all); it does NOT mean the job exited cleanly — and " +
	"exit_code 0 is a real value this field does report, on a run that claimed success and then failed its declared expectations. " +
	"A missing failure_signature or failed_step reads the same way: not known, never 'none'. " +
	"Then WRITE BACK what you found with add_incident_note, passing the incident_id from the entry you acted on. Reading the inbox " +
	"and saying nothing leaves the human exactly where they were."

// addIncidentNoteDesc is the write-back half. Two things have to survive here
// or the feature stops being worth having: notes are append-only (a
// correction is another note), and the agent writes back whether or not it
// fixed anything.
const addIncidentNoteDesc = "Write back, in your own words, what you found out about an incident — so the person who gets paged reads a diagnosis " +
	"instead of a timestamp: 'failed because the upstream API returned 503; same failure as the last three nights; I retried twice " +
	"and stopped' instead of 'check failed at 03:04'. The note appears on the incident's page in the dashboard, attributed to its " +
	"author, in the order it was written. Take incident_id from list_open_incidents. " +
	"SEND A NOTE WHETHER OR NOT YOU COULD FIX THE PROBLEM. The person reading the alert cannot see what you saw. With no note, an " +
	"incident is indistinguishable from one nobody has looked at yet, so an agent that writes back only its successes leaves a " +
	"record worse than none: every unexplained incident then reads as 'not looked at yet' when it may equally mean 'looked at and " +
	"gave up'. 'Could not reproduce; gave up after two attempts' IS a finding and is worth writing. " +
	"NOTES ARE APPEND-ONLY. There is no way to edit a note and no way to delete one — not merely unexposed: no route and no query " +
	"exists for either, and an edit is refused by the database itself. A correction is a new note, never an edit, because a " +
	"diagnosis whose history a reader cannot trust is not evidence. " +
	"This is NOT a write-once resource: a second, third or tenth note on the same incident is normal and expected, and there is no " +
	"conflict for writing one. The only conflict this tool has is the cap of 50 notes per incident, and reaching it means something " +
	"is looping rather than diagnosing. " +
	"A CLOSED incident still accepts notes, on purpose: the run that finally succeeded is usually the one that understood why the " +
	"previous one did not, so refusing the note would lose the explanation exactly when it became available. " +
	"Authorship is not yours to choose — every note written through this tool is stored as author 'agent', because this is the " +
	"API-key surface; there is no author argument and supplying one is not possible."

// addIncidentNoteBodyDesc describes the one field the request carries. The
// two caps are 400s the agent cannot anticipate unless it is told.
const addIncidentNoteBodyDesc = "The diagnosis, in plain words and one or two sentences: what actually failed, whether it is the same failure as before " +
	"(compare failure_signature.occurrences from list_open_incidents), and what you did about it. " +
	"Must not be empty or whitespace-only, and must be at most 8192 bytes. An oversized body is REJECTED, never truncated — a " +
	"truncated diagnosis reads as a complete one that trails off, and the reader cannot tell that the sentence naming the cause was " +
	"the one cut — so shorten it and call again. A pasted stack trace is a note nobody reads: the full failure output already lives " +
	"on the run that produced it."

// registerFailureInboxTools registers list_open_incidents and
// add_incident_note.
func registerFailureInboxTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("list_open_incidents",
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
		mcp.NewTool("add_incident_note",
			mcp.WithDescription(addIncidentNoteDesc),
			mcp.WithNumber("incident_id", mcp.Required(),
				mcp.Description("The incident's numeric id, taken straight from an entry's incident_id in list_open_incidents. An integer, not a UUID.")),
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

	out, mErr := json.MarshalIndent(incidents, "", "  ")
	if mErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to render response: %v", mErr)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
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
// declare (see api/incident_notes.go), and no created_at, because a note's
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
