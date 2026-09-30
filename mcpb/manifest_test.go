package mcpb_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type manifest struct {
	ManifestVersion string `json:"manifest_version"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command           string                    `json:"command"`
			Args              []string                  `json:"args"`
			Env               map[string]string         `json:"env"`
			PlatformOverrides map[string]map[string]any `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	UserConfig map[string]struct {
		Type      string `json:"type"`
		Sensitive bool   `json:"sensitive"`
		Required  bool   `json:"required"`
		Default   any    `json:"default"`
	} `json:"user_config"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
	PrivacyPolicies []string `json:"privacy_policies"`
}

func readManifest(t *testing.T) manifest {
	t.Helper()
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The key is the one secret this bundle handles. It must be a sensitive
// user_config field (Claude Desktop stores it in the OS keychain) and reach
// the binary only through the environment, never an argument (argv is
// visible to every local process).
func TestManifest_KeyIsSensitiveAndOnlyInEnv(t *testing.T) {
	m := readManifest(t)
	k, ok := m.UserConfig["api_key"]
	if !ok || !k.Sensitive || !k.Required || k.Type != "string" {
		t.Fatalf("user_config.api_key must be a required sensitive string, got %+v", k)
	}
	if k.Default != nil {
		t.Fatalf("user_config.api_key must carry no default, got %v", k.Default)
	}
	if m.Server.MCPConfig.Env["LASTPING_API_KEY"] != "${user_config.api_key}" {
		t.Fatalf("LASTPING_API_KEY must come from user_config.api_key")
	}
	// Walk the whole server object (command, args, env, every platform
	// override): the key reference must appear exactly once, at
	// mcp_config.env.LASTPING_API_KEY, so no command, argument, override or
	// second variable can carry it.
	var raw struct {
		Server any `json:"server"`
	}
	b, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var found []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if strings.Contains(k, "user_config") {
					found = append(found, path+" (key "+k+")")
				}
				walk(path+"."+k, e)
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case string:
			if strings.Contains(x, "user_config") {
				found = append(found, path)
			}
		}
	}
	walk("server", raw.Server)
	if len(found) != 1 || found[0] != "server.mcp_config.env.LASTPING_API_KEY" {
		t.Fatalf("user_config must appear in server only at mcp_config.env.LASTPING_API_KEY, found at %v", found)
	}
	if m.Server.Type != "binary" || len(m.PrivacyPolicies) == 0 {
		t.Fatalf("server.type binary and a privacy policy are required")
	}
	want := map[string]bool{"darwin": true, "win32": true}
	for _, p := range m.Compatibility.Platforms {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("missing platforms: %v", want)
	}
}
