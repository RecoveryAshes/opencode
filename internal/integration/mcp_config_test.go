package integration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfiguredMCPServersParsesLocalRemoteAndDisabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	content := []byte(`{
  "mcp": {
    "disabled": {"enabled": false},
    "local": {
      "type": "local",
      "command": ["go", "run", "server.go"],
      "environment": {"TOKEN": "secret"},
      "timeout": 1234
    },
    "remote": {
      "type": "remote",
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer token"}
    }
  }
}`)
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	servers, err := LoadConfiguredMCPServers(root)
	if err != nil {
		t.Fatalf("LoadConfiguredMCPServers() error = %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("servers = %#v, want three entries", servers)
	}
	if servers[0].Name != "disabled" || !servers[0].Disabled {
		t.Fatalf("disabled server = %#v, want disabled old-style entry", servers[0])
	}
	if servers[1].Name != "local" || servers[1].Config.Type != "local" || servers[1].Config.Command[0] != "go" || servers[1].Config.Environment["TOKEN"] != "secret" || servers[1].Config.Timeout != 1234 {
		t.Fatalf("local server = %#v, want parsed local config", servers[1])
	}
	if servers[2].Name != "remote" || servers[2].Config.Type != "remote" || servers[2].Config.URL != "https://example.com/mcp" || servers[2].Config.Headers["Authorization"] == "" {
		t.Fatalf("remote server = %#v, want parsed remote config", servers[2])
	}
}

func TestLoadConfiguredMCPServersRejectsInvalidConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{"mcp":{"bad":{"type":"local"}}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfiguredMCPServers(root)
	if err == nil {
		t.Fatalf("LoadConfiguredMCPServers() error = nil, want invalid config error")
	}
}
