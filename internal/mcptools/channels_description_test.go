package mcptools_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// wantDestinationKinds is the kind list copied from the hosted server, written
// out rather than read from destinationKinds so this test cannot pass by
// comparing the table to itself.
var wantDestinationKinds = []string{
	"webhook", "telegram", "discord", "slack", "ntfy",
	"pushover", "msteams", "googlechat", "email",
}

// wantHostPins is the host rule per pinned kind, likewise copied. The five
// unpinned kinds (webhook, telegram, ntfy, pushover, email) are absent
// deliberately: webhook and ntfy are covered by the unpinned assertions below,
// and telegram, pushover and email have no user-supplied host at all.
var wantHostPins = map[string][]string{
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

// paramDescription reads a parameter's description off the tool the server
// actually serves, rather than off the source, so a description that never
// reaches the wire fails here.
func paramDescription(t *testing.T, tool, param string) string {
	t.Helper()

	s := newTestServer(t, "https://ping.lastping.dev")
	st, ok := s.ListTools()[tool]
	require.True(t, ok, "tool %q is not registered", tool)

	raw, ok := st.Tool.InputSchema.Properties[param]
	require.True(t, ok, "tool %q has no %q parameter", tool, param)

	schema, ok := raw.(map[string]any)
	require.True(t, ok, "parameter %q of %q has an unexpected schema shape %T", param, tool, raw)

	desc, ok := schema["description"].(string)
	require.True(t, ok, "parameter %q of %q has no description", param, tool)
	return desc
}

// toolDescription lives in untrusted_test.go and reads the description off a
// real tools/list response.

// TestCreateDestination_KindParamNamesEveryKind — this binary enforces no host
// rule; the hosted API does. The description is therefore the ONLY thing that
// can stop an agent inventing a URL, and an agent that gets a bare 400 with no
// actionable rule retries the same call.
func TestCreateDestination_KindParamNamesEveryKind(t *testing.T) {
	desc := paramDescription(t, "create_destination", "kind")
	for _, k := range wantDestinationKinds {
		require.Contains(t, desc, k, "create_destination's kind param description is missing %q", k)
	}
}

// TestCreateDestination_KindParamCarriesTheHostRule pins every pinned kind's
// hosts, per kind, so a single host dropped from the list is named by the
// failure rather than hidden behind a passing Contains on the others.
func TestCreateDestination_KindParamCarriesTheHostRule(t *testing.T) {
	desc := paramDescription(t, "create_destination", "kind")

	require.Contains(t, desc, "must be https",
		"the https requirement applies to every kind and must be stated")

	for kind, pins := range wantHostPins {
		t.Run(kind, func(t *testing.T) {
			require.Contains(t, desc, kind+" at ",
				"the host rule must name %q as a pinned kind", kind)
			for _, pin := range pins {
				require.Contains(t, desc, pin,
					"%q is missing host %q — an agent told an incomplete list will be refused by the API",
					kind, pin)
			}
		})
	}
}

// TestCreateDestination_KindParamSaysWhatIsUnpinned is the positive companion:
// without it the host-rule test degrades into "the description mentions some
// hosts", and an agent with a self-hosted ntfy or an arbitrary collector would
// be left believing every kind is pinned.
func TestCreateDestination_KindParamSaysWhatIsUnpinned(t *testing.T) {
	desc := paramDescription(t, "create_destination", "kind")
	require.Contains(t, desc, "accepts any https host",
		"webhook is the escape hatch and the description must say so")
	require.Contains(t, desc, "ntfy is unpinned",
		"self-hosted ntfy is a shipped capability, not a refusal waiting to happen")
}

// TestCreateDestination_KindParamCarriesTheOwnershipCaveat is the assertion
// that matters most for how an agent REPORTS what it built. A pin narrows the
// target to the vendor's platform; every one of those domains is multi-tenant
// and open to anyone who signs up, so the pin proves nothing about who owns the endpoint. An
// agent that summarises "verified Slack destination" on the strength of the
// host has told its user something false.
func TestCreateDestination_KindParamCarriesTheOwnershipCaveat(t *testing.T) {
	desc := paramDescription(t, "create_destination", "kind")
	require.Contains(t, desc, "does NOT prove the endpoint belongs to")
	require.Contains(t, desc, "multi-tenant and open to anyone who signs up")
	require.Contains(t, desc, "Do not report a pinned destination as verified")
}

// TestCreateDestination_DescribesThePerProjectCap — the cap is a refusal an
// agent will hit while creating, and DESTINATION_CAP_REACHED is not
// self-explanatory. Told to retry, an agent retries; told the number and the
// remedy, it deletes one.
func TestCreateDestination_DescribesThePerProjectCap(t *testing.T) {
	desc := toolDescription(t, "create_destination")
	require.Contains(t, desc, "at most 25 destinations")
	require.Contains(t, desc, "DESTINATION_CAP_REACHED")
	require.Contains(t, desc, "delete one with delete_destination rather than retrying")
}

// TestUpdateDestination_ConfigParamCarriesTheRules — update_destination names
// the kind list a second time and re-runs the same host check server-side, so
// the same rules have to be stated here. The strict-keys rule is only on this
// parameter: a config naming a field its kind does not accept is refused.
func TestUpdateDestination_ConfigParamCarriesTheRules(t *testing.T) {
	desc := paramDescription(t, "update_destination", "config")

	for _, k := range wantDestinationKinds {
		require.Contains(t, desc, k, "update_destination's config param description is missing %q", k)
	}
	require.Contains(t, desc, "must be https")
	require.Contains(t, desc, "only the fields listed for its kind, each exactly once",
		"the strict-keys rule is what turns a typo'd field into a 400 rather than a silent no-op")
	require.Contains(t, desc, "does NOT prove the endpoint belongs to",
		"the ownership caveat has to be on both destination-writing tools")
}

// TestDestinationTools_KindListIsNotEmpty guards the generated sentence itself.
// kindHostPinSentence returns an empty string when no kind is pinned, and an
// empty string concatenated into a description is invisible — the description
// would read as though there were no host rule at all, which is the exact
// misinformation these tests exist to prevent.
func TestDestinationTools_KindListIsNotEmpty(t *testing.T) {
	desc := paramDescription(t, "create_destination", "kind")
	require.Contains(t, desc, "A BRANDED kind must point at its vendor's host: ")
	require.NotContains(t, desc, "vendor's host: For any other endpoint",
		"the generated host-pin sentence collapsed to nothing")
}

// TestDestinationTools_ScopeSentenceStillLeads — the destination descriptions
// were rewritten above, and the scope prefix is applied by newTool to whatever
// text the call site passes. This pins that the rewrite did not displace it.
func TestDestinationTools_ScopeSentenceStillLeads(t *testing.T) {
	for _, tc := range []struct{ tool, scope string }{
		{"list_destinations", "read"},
		{"create_destination", "write"},
		{"update_destination", "write"},
		{"test_destination", "write"},
		{"delete_destination", "write"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			desc := toolDescription(t, tc.tool)
			want := "Requires an API key with the " + tc.scope + " scope or higher."
			require.True(t, strings.HasPrefix(desc, want),
				"%s must lead with %q, got: %.80q", tc.tool, want, desc)
		})
	}
}
