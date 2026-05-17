package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// PTYInfo is the public pseudo-terminal session DTO.
type PTYInfo struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	CWD     string   `json:"cwd"`
	Status  string   `json:"status"`
	PID     int      `json:"pid"`
	Size    *PTYSize `json:"size,omitempty"`
}

// PTYCreateInput creates a shell-backed process. It is not a real OS PTY yet,
// but preserves the HTTP lifecycle contract and stream buffer for local use.
type PTYCreateInput struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Title   string            `json:"title,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// PTYUpdateInput changes mutable PTY session fields.
type PTYUpdateInput struct {
	Title string   `json:"title,omitempty"`
	Size  *PTYSize `json:"size,omitempty"`
}

// PTYSize records terminal dimensions from the public PTY update contract.
type PTYSize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// PTYManager manages local shell sessions.
type PTYManager struct {
	mu       sync.Mutex
	sessions map[string]*ptySession
	tickets  map[string]ptyTicket
}

type ptySession struct {
	info   PTYInfo
	cmd    *exec.Cmd
	stdin  ioWriteCloser
	buffer safeBuffer
}

type ioWriteCloser interface {
	Write([]byte) (int, error)
	Close() error
}

type ptyTicket struct {
	PTYID     string
	ExpiresAt time.Time
}

// NewPTYManager creates an empty PTY manager.
func NewPTYManager() *PTYManager {
	return &PTYManager{
		sessions: map[string]*ptySession{},
		tickets:  map[string]ptyTicket{},
	}
}

// Shells lists common local shells.
func Shells() []map[string]any {
	candidates := []string{os.Getenv("SHELL"), "/bin/zsh", "/bin/bash", "/bin/sh"}
	if runtime.GOOS == "windows" {
		candidates = []string{os.Getenv("COMSPEC"), "powershell.exe", "cmd.exe"}
	}
	seen := map[string]bool{}
	result := []map[string]any{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		acceptable := true
		if strings.Contains(candidate, "/") {
			if _, err := os.Stat(candidate); err != nil {
				acceptable = false
			}
		}
		result = append(result, map[string]any{
			"path":       candidate,
			"name":       candidateName(candidate),
			"acceptable": acceptable,
		})
	}
	return result
}

// List returns all known PTY sessions.
func (manager *PTYManager) List() []PTYInfo {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := []PTYInfo{}
	for _, session := range manager.sessions {
		result = append(result, session.info)
	}
	return result
}

// Get returns one PTY session.
func (manager *PTYManager) Get(id string) (PTYInfo, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[id]
	if session == nil {
		return PTYInfo{}, false
	}
	return session.info, true
}

// Create starts a shell process and captures combined output.
func (manager *PTYManager) Create(ctx context.Context, input PTYCreateInput) (PTYInfo, error) {
	command := input.Command
	if command == "" {
		command = os.Getenv("SHELL")
	}
	if command == "" {
		command = "/bin/sh"
	}
	cwd := input.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			cwd = "."
		}
	}
	id := fmt.Sprintf("pty_%x", time.Now().UnixNano())
	title := input.Title
	if title == "" {
		title = "Terminal " + id[len(id)-4:]
	}
	_ = ctx
	cmd := exec.Command(command, input.Args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for key, value := range input.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = upsertEnv(cmd.Env, "TERM", "xterm-256color")
	cmd.Env = upsertEnv(cmd.Env, "OPENCODE_TERMINAL", "1")
	if runtime.GOOS == "windows" {
		cmd.Env = upsertEnv(cmd.Env, "LC_ALL", "C.UTF-8")
		cmd.Env = upsertEnv(cmd.Env, "LC_CTYPE", "C.UTF-8")
		cmd.Env = upsertEnv(cmd.Env, "LANG", "C.UTF-8")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return PTYInfo{}, fmt.Errorf("pty stdin: %w", err)
	}
	session := &ptySession{}
	cmd.Stdout = &session.buffer
	cmd.Stderr = &session.buffer
	if err := cmd.Start(); err != nil {
		return PTYInfo{}, fmt.Errorf("start pty command: %w", err)
	}
	session.stdin = stdin
	session.cmd = cmd
	session.info = PTYInfo{
		ID:      id,
		Title:   title,
		Command: command,
		Args:    append([]string(nil), input.Args...),
		CWD:     cwd,
		Status:  "running",
		PID:     cmd.Process.Pid,
	}
	manager.mu.Lock()
	manager.sessions[id] = session
	manager.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		manager.mu.Lock()
		if current := manager.sessions[id]; current != nil {
			current.info.Status = "exited"
		}
		manager.mu.Unlock()
	}()
	return session.info, nil
}

// Update changes PTY metadata.
func (manager *PTYManager) Update(id string, input PTYUpdateInput) (PTYInfo, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[id]
	if session == nil {
		return PTYInfo{}, false
	}
	if input.Title != "" {
		session.info.Title = input.Title
	}
	if input.Size != nil {
		session.info.Size = &PTYSize{Rows: input.Size.Rows, Cols: input.Size.Cols}
	}
	return session.info, true
}

// Remove terminates and deletes one PTY session.
func (manager *PTYManager) Remove(id string) bool {
	manager.mu.Lock()
	session := manager.sessions[id]
	if session != nil {
		delete(manager.sessions, id)
	}
	manager.mu.Unlock()
	if session == nil {
		return false
	}
	if session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}
	_ = session.stdin.Close()
	return true
}

// Write sends input to the process.
func (manager *PTYManager) Write(id string, data string) error {
	manager.mu.Lock()
	session := manager.sessions[id]
	manager.mu.Unlock()
	if session == nil {
		return fmt.Errorf("pty session %q not found", id)
	}
	if _, err := session.stdin.Write([]byte(data)); err != nil {
		return fmt.Errorf("write pty input: %w", err)
	}
	return nil
}

// Buffer returns captured process output.
func (manager *PTYManager) Buffer(id string) (string, bool) {
	manager.mu.Lock()
	session := manager.sessions[id]
	manager.mu.Unlock()
	if session == nil {
		return "", false
	}
	return session.buffer.String(), true
}

// IssueConnectToken creates a short-lived single-use ticket for PTY WebSocket connection.
func (manager *PTYManager) IssueConnectToken(id string) (map[string]any, bool, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.sessions[id] == nil {
		return nil, false, nil
	}
	token, err := randomPTYToken(16)
	if err != nil {
		return nil, true, err
	}
	manager.tickets[token] = ptyTicket{PTYID: id, ExpiresAt: time.Now().Add(time.Minute)}
	return map[string]any{"ticket": token, "expires_in": 60}, true, nil
}

// ConsumeConnectToken validates and consumes a PTY WebSocket ticket.
func (manager *PTYManager) ConsumeConnectToken(id string, token string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	ticket, ok := manager.tickets[token]
	if !ok {
		return false
	}
	delete(manager.tickets, token)
	return ticket.PTYID == id && time.Now().Before(ticket.ExpiresAt)
}

func candidateName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func upsertEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for index, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func randomPTYToken(bytesLen int) (string, error) {
	data := make([]byte, bytesLen)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate pty token: %w", err)
	}
	return hex.EncodeToString(data), nil
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *safeBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *safeBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}
