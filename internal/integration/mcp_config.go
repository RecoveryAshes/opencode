package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/config"
)

// MCPConfiguredServer is one MCP entry loaded from opencode config.
type MCPConfiguredServer struct {
	Name     string    `json:"name"`
	Config   MCPConfig `json:"config,omitempty"`
	Disabled bool      `json:"disabled,omitempty"`
}

// LoadConfiguredMCPServers reads configured MCP servers from local config.
func LoadConfiguredMCPServers(directory string) ([]MCPConfiguredServer, error) {
	cfg, err := config.Load(config.LoadOptions{Directory: directory})
	if err != nil {
		return nil, err
	}
	raw, ok := cfg.Info["mcp"].(map[string]any)
	if !ok || len(raw) == 0 {
		return []MCPConfiguredServer{}, nil
	}
	result := make([]MCPConfiguredServer, 0, len(raw))
	for name, value := range raw {
		server, err := parseMCPConfiguredServer(name, value)
		if err != nil {
			return nil, err
		}
		if server.Name != "" {
			result = append(result, server)
		}
	}
	slices.SortFunc(result, func(a MCPConfiguredServer, b MCPConfiguredServer) int {
		return strings.Compare(a.Name, b.Name)
	})
	return result, nil
}

func parseMCPConfiguredServer(name string, raw any) (MCPConfiguredServer, error) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return MCPConfiguredServer{}, nil
	}
	enabled, hasEnabled := entry["enabled"].(bool)
	server := MCPConfiguredServer{Name: name}
	if hasEnabled && !enabled {
		server.Disabled = true
	}
	if _, ok := entry["type"].(string); !ok {
		if server.Disabled {
			return server, nil
		}
		return MCPConfiguredServer{}, fmt.Errorf("mcp server %q is missing type", name)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return MCPConfiguredServer{}, fmt.Errorf("encode mcp server %q: %w", name, err)
	}
	if err := json.Unmarshal(data, &server.Config); err != nil {
		return MCPConfiguredServer{}, fmt.Errorf("decode mcp server %q: %w", name, err)
	}
	switch server.Config.Type {
	case "local":
		if len(server.Config.Command) == 0 && !server.Disabled {
			return MCPConfiguredServer{}, fmt.Errorf("mcp server %q local command is required", name)
		}
	case "remote":
		if server.Config.URL == "" && !server.Disabled {
			return MCPConfiguredServer{}, fmt.Errorf("mcp server %q remote url is required", name)
		}
	default:
		return MCPConfiguredServer{}, fmt.Errorf("mcp server %q has unsupported type %q", name, server.Config.Type)
	}
	return server, nil
}
