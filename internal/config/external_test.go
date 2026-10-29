package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// External configuration is explicit and survives a settings round trip. The
// test also pins the shape that keeps credentials out of the file: only an
// environment variable name is accepted.
func TestExternalSettingsRoundTrip(t *testing.T) {
	original := Settings{
		Search: &SearchSettings{
			DefaultProvider: "brave",
			Providers: []SearchProvider{{
				Name: "brave", Kind: "brave", Endpoint: "https://api.example/search",
				APIKeyEnv: "BRAVE_KEY", MaxResults: 5, Retries: 1,
			}},
		},
		MCP: &MCPSettings{Servers: []MCPServer{{
			Name: "alpha", Command: "/bin/echo", Args: []string{"serve"},
			Env: []string{"MCP_TOKEN"}, Tools: map[string]string{"read": "read", "send": "mutate"},
			Resources: []string{"file:///docs/"}, AllowMutation: true, MaxBytes: 4096,
		}}},
		Browser: &BrowserSettings{
			AllowedHosts: []string{"example.com"}, MaxNavigations: 4, MaxRedirects: 2,
			MaxBytes: 8192, MaxChars: 2048, MaxLinks: 10, MaxSessions: 2,
		},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	// The settings document must not contain a credential value. The env name
	// is metadata, not a secret.
	if strings.Contains(string(encoded), "secret-value") {
		t.Fatal("a credential value was serialized")
	}
	var decoded Settings
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Search == nil || decoded.Search.Providers[0].APIKeyEnv != "BRAVE_KEY" {
		t.Fatalf("search settings lost: %+v", decoded.Search)
	}
	if decoded.MCP == nil || decoded.MCP.Servers[0].Tools["send"] != "mutate" ||
		!decoded.MCP.Servers[0].AllowMutation || decoded.MCP.Servers[0].Env[0] != "MCP_TOKEN" {
		t.Fatalf("mcp settings lost: %+v", decoded.MCP)
	}
	if decoded.Browser == nil || decoded.Browser.AllowedHosts[0] != "example.com" ||
		decoded.Browser.MaxSessions != 2 {
		t.Fatalf("browser settings lost: %+v", decoded.Browser)
	}
}

func TestExternalSettingsAreAbsentByDefault(t *testing.T) {
	settings := Default()
	if settings.Search != nil || settings.MCP != nil || settings.Browser != nil {
		t.Fatalf("default settings expose an external capability: %+v", settings)
	}
}
