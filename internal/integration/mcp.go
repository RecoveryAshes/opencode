package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// MCPConfig describes a local or remote MCP server entry.
type MCPConfig struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// MCPStatus is the public status shape used by /mcp.
type MCPStatus struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// MCPToolDef is a listed MCP tool.
type MCPToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// MCPManager manages MCP server configs and local stdio clients.
type MCPManager struct {
	mu      sync.Mutex
	configs map[string]MCPConfig
	clients map[string]*mcpClient
	status  map[string]MCPStatus
}

// NewMCPManager creates an empty MCP manager.
func NewMCPManager() *MCPManager {
	return &MCPManager{
		configs: map[string]MCPConfig{},
		clients: map[string]*mcpClient{},
		status:  map[string]MCPStatus{},
	}
}

// Status returns every configured MCP server status.
func (manager *MCPManager) Status() map[string]MCPStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := map[string]MCPStatus{}
	for name := range manager.configs {
		result[name] = manager.status[name]
		if result[name].Status == "" {
			result[name] = MCPStatus{Status: "disabled"}
		}
	}
	return result
}

// Add stores and connects an MCP server.
func (manager *MCPManager) Add(ctx context.Context, name string, config MCPConfig) map[string]MCPStatus {
	manager.mu.Lock()
	manager.configs[name] = config
	manager.mu.Unlock()
	_ = manager.Connect(ctx, name)
	return manager.Status()
}

// Connect connects one configured local MCP server.
func (manager *MCPManager) Connect(ctx context.Context, name string) error {
	manager.mu.Lock()
	config, ok := manager.configs[name]
	if !ok {
		manager.mu.Unlock()
		return fmt.Errorf("mcp config %q not found", name)
	}
	if config.Enabled != nil && !*config.Enabled {
		manager.status[name] = MCPStatus{Status: "disabled"}
		manager.mu.Unlock()
		return nil
	}
	if old := manager.clients[name]; old != nil {
		_ = old.Close()
		delete(manager.clients, name)
	}
	manager.mu.Unlock()

	if config.Type == "remote" {
		status := manager.checkRemote(ctx, config)
		manager.mu.Lock()
		manager.status[name] = status
		manager.mu.Unlock()
		return nil
	}
	client, err := startMCPClient(ctx, config)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err != nil {
		manager.status[name] = MCPStatus{Status: "failed", Error: err.Error()}
		return err
	}
	if _, err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		manager.status[name] = MCPStatus{Status: "failed", Error: err.Error()}
		return err
	}
	manager.clients[name] = client
	manager.status[name] = MCPStatus{Status: "connected"}
	return nil
}

// Disconnect stops one MCP server.
func (manager *MCPManager) Disconnect(name string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if client := manager.clients[name]; client != nil {
		_ = client.Close()
		delete(manager.clients, name)
	}
	manager.status[name] = MCPStatus{Status: "disabled"}
}

// Tools lists tools from a connected MCP server.
func (manager *MCPManager) Tools(ctx context.Context, name string) ([]MCPToolDef, error) {
	client, err := manager.client(name)
	if err != nil {
		return nil, err
	}
	return client.ListTools(ctx)
}

// CallTool calls one tool on a connected MCP server.
func (manager *MCPManager) CallTool(ctx context.Context, name string, tool string, args map[string]any) (map[string]any, error) {
	client, err := manager.client(name)
	if err != nil {
		return nil, err
	}
	return client.CallTool(ctx, tool, args)
}

func (manager *MCPManager) client(name string) (*mcpClient, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	client := manager.clients[name]
	if client == nil {
		return nil, fmt.Errorf("mcp client %q is not connected", name)
	}
	return client, nil
}

func (manager *MCPManager) checkRemote(ctx context.Context, config MCPConfig) MCPStatus {
	if config.URL == "" {
		return MCPStatus{Status: "failed", Error: "remote mcp url is required"}
	}
	timeout := mcpTimeout(config.Timeout)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, config.URL, nil)
	if err != nil {
		return MCPStatus{Status: "failed", Error: err.Error()}
	}
	for key, value := range config.Headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return MCPStatus{Status: "failed", Error: err.Error()}
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode >= 200 && response.StatusCode < 500 {
		return MCPStatus{Status: "connected"}
	}
	return MCPStatus{Status: "failed", Error: fmt.Sprintf("remote status %d", response.StatusCode)}
}

type mcpClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	timeout time.Duration
	mu      sync.Mutex
	nextID  int
}

func startMCPClient(ctx context.Context, config MCPConfig) (*mcpClient, error) {
	_ = ctx
	if config.Type != "local" {
		return nil, fmt.Errorf("only local MCP stdio clients are executable in Go runtime")
	}
	if len(config.Command) == 0 {
		return nil, fmt.Errorf("local MCP command is required")
	}
	cmd := exec.Command(config.Command[0], config.Command[1:]...)
	cmd.Env = os.Environ()
	for key, value := range config.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mcp command: %w", err)
	}
	return &mcpClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		timeout: mcpTimeout(config.Timeout),
	}, nil
}

func (client *mcpClient) Initialize(ctx context.Context) (map[string]any, error) {
	result, err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "opencode-go", "version": "go-migration"},
	})
	if err != nil {
		return nil, err
	}
	_ = client.notify(ctx, "notifications/initialized", map[string]any{})
	return result, nil
}

func (client *mcpClient) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	result, err := client.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(result["tools"])
	var tools []MCPToolDef
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("decode mcp tools: %w", err)
	}
	return tools, nil
}

func (client *mcpClient) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	return client.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
}

func (client *mcpClient) Close() error {
	_ = client.stdin.Close()
	if client.cmd.Process != nil {
		_ = client.cmd.Process.Kill()
	}
	return client.cmd.Wait()
}

func (client *mcpClient) notify(ctx context.Context, method string, params map[string]any) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	message := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return client.write(ctx, message)
}

func (client *mcpClient) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.nextID++
	id := client.nextID
	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := client.write(ctx, message); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(client.timeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("mcp request %s timed out", method)
		}
		line, err := client.stdout.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read mcp response: %w", err)
		}
		var response struct {
			ID     any            `json:"id"`
			Result map[string]any `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			continue
		}
		if fmt.Sprint(response.ID) != fmt.Sprint(id) {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("mcp %s failed: %s", method, response.Error.Message)
		}
		return response.Result, nil
	}
}

func (client *mcpClient) write(ctx context.Context, message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode mcp request: %w", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := client.stdin.Write(append(data, '\n'))
		writeDone <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-writeDone:
		if err != nil {
			return fmt.Errorf("write mcp request: %w", err)
		}
		return nil
	}
}

func mcpTimeout(ms int) time.Duration {
	if ms <= 0 {
		return 5 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}
