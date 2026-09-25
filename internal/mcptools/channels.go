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

// destinationKinds is the ordered list of destination kinds the hosted API
// accepts, and destinationHostPins is the host rule it applies to the branded
// ones. Both mirror the hosted server; nothing here is enforced by this binary
// — a wrong URL is refused by the API, not by us. They exist so the tool
// DESCRIPTIONS can be generated from one list instead of nine hand-typed
// copies, which is how the kind list drifted before.
//
// The order is the one the dashboard dropdown and the OpenAPI enum render in.
var destinationKinds = []string{
	"webhook", "telegram", "discord", "slack", "ntfy",
	"pushover", "msteams", "googlechat", "email",
}

// destinationHostPins maps a BRANDED kind to the host suffixes its URL may
// point at. A kind absent from this map is deliberately NOT pinned:
//
//   - webhook is the generic escape hatch — any https host, by design. It is
//     what an owner is told to switch to if their branded URL is off-host.
//   - ntfy self-hosting is a documented, intentional feature; pinning it to
//     ntfy.sh would delete a shipped capability.
//
// The Microsoft list is four generations of the same product deep: the
// original Office 365 connector webhook, its per-tenant successor, the first
// Power Automate "Workflows" replacement on logic.azure.com (.us for GCC
// High), and what Power Automate issues now on
// <env>.environment.api.powerplatform.com.
//
// What a pin buys, stated honestly: it narrows a branded destination's target
// to the vendor's own platform. It does NOT prove the endpoint belongs to the
// owner who created it — every one of these domains is multi-tenant and
// self-service. What it removes is "any host on the internet" as the target of
// a destination LABELLED with a vendor's name.
var destinationHostPins = map[string][]string{
	"slack":   {"hooks.slack.com"},
	"discord": {"discord.com", "discordapp.com"},
	"msteams": {
		"webhook.office.com",
		"outlook.office.com",
		"logic.azure.com",
		"logic.azure.us",
		"environment.api.powerplatform.com",
	},
	"googlechat": {"chat.googleapis.com"},
}

// kindHostPinSentence renders the per-kind host pins for a tool description,
// DERIVED from destinationHostPins so an agent is never told a rule in one
// place and a different one in another. It ends with a trailing space, so the
// caller can append.
//
// It exists because the MCP client surfaces only a problem's detail and drops
// its fix, so the remediation an agent needs has to be in the tool description
// BEFORE it guesses a URL — an agent that gets "400" with no actionable rule
// retries the same call.
func kindHostPinSentence() string {
	var parts []string
	for _, k := range destinationKinds {
		if pins := destinationHostPins[k]; len(pins) > 0 {
			parts = append(parts, k+" at "+strings.Join(pins, " or "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + ". "
}

// Channel mirrors the LastPing Channel resource (no secrets).
type Channel struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Target        string `json:"target"`
	Verified      bool   `json:"verified"`
	Disabled      bool   `json:"disabled"`
	DisableReason string `json:"disable_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
}

func registerChannelTools(s *server.MCPServer) {
	s.AddTool(
		newTool("list_destinations",
			mcp.WithDescription("List all notification destinations (channels) in the project: email, webhook, Slack, Discord, Telegram. "+
				"Use channel IDs to configure routing rules for monitors.")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.listChannels(ctx)
		},
	)

	s.AddTool(
		newTool("create_destination",
			mcp.WithDescription("Create a notification destination (channel) that monitors can route alerts to. "+
				"Provide the fields for the chosen kind; unrelated fields are ignored. Non-email kinds are usable "+
				"immediately; email kinds are created unverified and send a confirmation link that must be clicked "+
				"before they can be attached to a route. A project holds at most 25 destinations — if creation is "+
				"refused with DESTINATION_CAP_REACHED, delete one with delete_destination rather than retrying. "+
				"Returns the new channel id — pass it to set_route."),
			mcp.WithString("kind", mcp.Required(), mcp.Description(
				"One of: "+strings.Join(destinationKinds, ", ")+". "+
					"Every destination URL must be https. A BRANDED kind must point at its vendor's host: "+
					kindHostPinSentence()+
					"For any other endpoint use kind \"webhook\", which accepts any https host; ntfy is unpinned too, "+
					"so a self-hosted ntfy server is fine. A pin narrows the destination to the vendor's own platform; "+
					"it does NOT prove the endpoint belongs to the person or project that created it, because every "+
					"pinned domain is multi-tenant and open to anyone who signs up. Do not report a pinned destination as verified or "+
					"as owned by anyone on the strength of its host.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable destination name, e.g. 'On-call Slack'.")),
			mcp.WithString("url", mcp.Description("webhook: the POST target URL.")),
			mcp.WithString("secret", mcp.Description("webhook: shared secret used to sign the HMAC-SHA256 payload.")),
			mcp.WithString("webhook_url", mcp.Description("slack / discord / msteams / googlechat: the incoming-webhook URL.")),
			mcp.WithString("topic_url", mcp.Description("ntfy: the full topic URL, e.g. 'https://ntfy.sh/my-topic'.")),
			mcp.WithString("bot_token", mcp.Description("telegram: the bot token from @BotFather.")),
			mcp.WithString("chat_id", mcp.Description("telegram: the target chat id.")),
			mcp.WithString("token", mcp.Description("pushover: the application API token.")),
			mcp.WithString("user_key", mcp.Description("pushover: the user or group key.")),
			mcp.WithString("address", mcp.Description("email: the destination email address (a confirmation link is sent).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.createChannel(ctx, req)
		},
	)

	s.AddTool(
		newTool("update_destination",
			mcp.WithDescription("Update a notification destination's name and/or config in place. Only the fields you pass are changed. "+
				"The destination kind cannot be changed — delete and recreate instead. Changing an email destination's address "+
				"resets verification and sends a new confirmation email."),
			mcp.WithString("destination_id", mcp.Required(), mcp.Description("UUID of the destination to update. Get it from list_destinations.")),
			mcp.WithString("name", mcp.Description("New human-readable label. Omit to leave unchanged.")),
			mcp.WithObject("config", mcp.Description(
				"Replacement config for the destination's existing kind — one of "+strings.Join(destinationKinds, ", ")+
					". Shape must match the kind: {\"url\":…,\"secret\":…} for webhook, "+
					"{\"bot_token\":…,\"chat_id\":…} for telegram, {\"webhook_url\":…} for slack/discord/msteams/googlechat, {\"topic_url\":…} for ntfy, "+
					"{\"token\":…,\"user_key\":…} for pushover, {\"address\":…} for email. Omit to leave unchanged. "+
					"A URL you supply is re-checked against the kind's allowed hosts and must be https; a destination "+
					"created before that rule keeps working until you send a new config for it. The config must name "+
					"only the fields listed for its kind, each exactly once. The host rule narrows a branded "+
					"destination to the vendor's own platform; it does NOT prove the endpoint belongs to the person "+
					"or project that owns the destination.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := clientFromContext(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			id, err := req.RequireString("destination_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return c.updateChannel(ctx, id, req)
		},
	)

	// test_destination carries resend_verification rather than that being its
	// own tool: both arguments are the same request — "send something to this
	// destination now so it can be moved forward" — and they are mutually
	// exclusive in practice, because an unverified email destination is exactly
	// the one a test alert cannot reach. Splitting them would add a tool whose
	// only distinguishing feature is which of two sends it performs.
	s.AddTool(
		newTool("test_destination",
			mcp.WithDescription("Send something through a destination right now, to move it from 'created' to 'known to work'. "+
				"By default it delivers a synthetic 'LastPing test alert' immediately — use that after create_destination to confirm the credentials are right. "+
				"For an EMAIL destination that is still unverified, a test alert is not what you need: an unverified email cannot be attached to a route at all, "+
				"and no amount of testing changes that. Pass resend_verification=true instead to re-send the confirmation link a human must click. "+
				"That is the tool to reach for when create_destination reported UNVERIFIED and the confirmation email never arrived or has expired."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Destination (channel) UUID. Get it from list_destinations or create_destination.")),
			mcp.WithBoolean("resend_verification", mcp.Description("Set true to re-send the email confirmation link INSTEAD of a test alert. "+
				"Email destinations only — any other kind returns 400. Safe to repeat, and idempotent: on an already-verified destination it reports "+
				"verified and sends nothing rather than mailing the user again.")),
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
			if resend, _ := req.GetArguments()["resend_verification"].(bool); resend {
				return c.resendChannelVerification(ctx, id)
			}
			return c.testChannel(ctx, id)
		},
	)

	// delete_destination is a new tool rather than an argument on
	// update_destination: deletion is not a field value, and folding an
	// irreversible action into the tool an agent reaches for to rename things
	// is how an agent deletes a destination it meant to edit. Every other
	// resource in this surface pairs its update tool with a separate delete
	// (delete_monitor, delete_agent, revoke_api_key); this closes the one place
	// where an agent could create a destination through MCP and then had no way
	// to remove it.
	s.AddTool(
		newTool("delete_destination",
			mcp.WithDescription("Permanently delete a notification destination (channel). This cannot be undone. "+
				"It also removes the destination from every monitor's routing — any event type routed ONLY to this destination stops notifying anyone, "+
				"silently and with no incident to show for it. Before deleting a destination that is in use, check which monitors route to it "+
				"(get_monitor returns a monitor's `routes`) and give those event types another destination first. "+
				"To stop using a destination temporarily, prefer editing the routes with set_route and leaving the destination in place."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Destination (channel) UUID. Get it from list_destinations.")),
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
			return c.deleteChannel(ctx, id)
		},
	)
}

func (c *APIClient) listChannels(ctx context.Context) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/channels", nil)
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

	var channels []Channel
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if len(channels) == 0 {
		return mcp.NewToolResultText("No destinations found. Add one with create_destination."), nil
	}

	out, _ := json.MarshalIndent(channels, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

// channelConfig assembles the per-kind config object the API expects from the
// flat tool arguments. Only fields relevant to the kind are included; the API
// performs authoritative validation and returns a 400 for missing fields.
func channelConfig(kind string, args map[string]interface{}) map[string]interface{} {
	str := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	cfg := map[string]interface{}{}
	switch kind {
	case "webhook":
		cfg["url"] = str("url")
		cfg["secret"] = str("secret")
	case "telegram":
		cfg["bot_token"] = str("bot_token")
		cfg["chat_id"] = str("chat_id")
	case "slack", "discord", "msteams", "googlechat":
		cfg["webhook_url"] = str("webhook_url")
	case "ntfy":
		cfg["topic_url"] = str("topic_url")
	case "pushover":
		cfg["token"] = str("token")
		cfg["user_key"] = str("user_key")
	case "email":
		cfg["address"] = str("address")
	}
	return cfg
}

func (c *APIClient) createChannel(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	kind, _ := args["kind"].(string)
	name, _ := args["name"].(string)

	body := map[string]interface{}{
		"kind":   kind,
		"name":   name,
		"config": channelConfig(kind, args),
	}
	data, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/channels", bytes.NewReader(data))
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

	var ch Channel
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	if ch.Kind == "email" && !ch.Verified {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Destination created (id=%s, kind=email, name=%q) but UNVERIFIED — a confirmation link was emailed to %s. "+
				"It must be clicked before the destination can be attached to a route with set_route. "+
				"If it never arrives or expires, call test_destination with resend_verification=true to send another.",
			ch.ID, ch.Name, ch.Target)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Destination created (id=%s, kind=%s, name=%q). Ready to use — attach it to a monitor with set_route, or send a test with test_destination.",
		ch.ID, ch.Kind, ch.Name)), nil
}

// updateChannel issues PATCH /api/v1/channels/{id}. Only fields the caller
// supplied are sent, so omitted fields are left unchanged server-side.
func (c *APIClient) updateChannel(ctx context.Context, id string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	body := map[string]interface{}{}
	if v, ok := args["name"].(string); ok && v != "" {
		body["name"] = v
	}
	if v, ok := args["config"]; ok && v != nil {
		body["config"] = v
	}
	if len(body) == 0 {
		return mcp.NewToolResultError("nothing to update: provide name and/or config"), nil
	}

	data, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/channels/"+id, bytes.NewReader(data))
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
		return mcp.NewToolResultError(fmt.Sprintf("Destination not found: id=%s. Use list_destinations to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var ch Channel
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", err)), nil
	}

	out, _ := json.MarshalIndent(ch, "", "  ")
	return mcp.NewToolResultText(fmt.Sprintf("Destination updated:\n%s", out)), nil
}

func (c *APIClient) testChannel(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/channels/"+id+"/test", nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Destination not found: id=%s. Use list_destinations to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Test alert delivered successfully through destination %s.", id)), nil
}

// resendChannelVerification re-sends an email destination's confirmation link.
// The endpoint is idempotent: on an already-verified destination it reports
// {"resent":false,"verified":true} and sends nothing, which is reported back as
// the good news it is rather than as a no-op the agent might retry.
func (c *APIClient) resendChannelVerification(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/channels/"+id+"/resend-verification", nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Destination not found: id=%s. Use list_destinations to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusOK {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}

	var out struct {
		Resent   bool `json:"resent"`
		Verified bool `json:"verified"`
	}
	if dErr := json.NewDecoder(resp.Body).Decode(&out); dErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode response: %v", dErr)), nil
	}

	if out.Verified && !out.Resent {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Destination %s is ALREADY VERIFIED — nothing was sent. It can be attached to a route with set_route now.", id)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Confirmation link re-sent for destination %s. It stays UNVERIFIED, and cannot be attached to a route, until a human clicks the link in that email. "+
			"Nothing you can do from here completes the verification.", id)), nil
}

func (c *APIClient) deleteChannel(ctx context.Context, id string) (*mcp.CallToolResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/channels/"+id, nil)
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
		return mcp.NewToolResultError(fmt.Sprintf("Destination not found: id=%s. Use list_destinations to find valid IDs.", id)), nil
	}
	if resp.StatusCode != http.StatusNoContent {
		return mcp.NewToolResultError(c.problem(resp).Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Destination %s deleted. It has also been removed from every monitor's routing — "+
			"re-check any monitor that relied on it with get_monitor, and re-point the affected event types with set_route.", id)), nil
}
