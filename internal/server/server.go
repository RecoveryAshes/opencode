// Package server contains the Go headless HTTP sidecar.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RecoveryAshes/opencode/internal/config"
	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/runtime"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

// Options configures the Go sidecar listener.
type Options struct {
	Hostname  string
	Port      int
	Version   string
	Sessions  session.Repository
	Messages  session.MessageRepository
	Runtime   *runtime.PromptRuntime
	MCP       *integration.MCPManager
	PTY       *integration.PTYManager
	Interact  *integration.InteractionManager
	Workspace *integration.WorkspaceStore
	Events    *eventBus
}

// SessionRepository is the storage contract required by the HTTP server.
type SessionRepository = session.Repository

// MessageRepository is the message storage contract required by the HTTP server.
type MessageRepository = session.MessageRepository

// Listener describes a running sidecar server.
type Listener struct {
	URL    *url.URL
	server *http.Server
}

// Listen starts the Go sidecar HTTP server.
func Listen(ctx context.Context, opts Options) (*Listener, error) {
	handler := NewHandler(opts)
	tcpListener, err := listenTCP(opts.Hostname, opts.Port)
	if err != nil {
		return nil, err
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = server.Close()
		}
	}()
	go func() {
		if err := server.Serve(tcpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = server.Close()
		}
	}()

	addr, ok := tcpListener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected listener address %T", tcpListener.Addr())
	}
	return &Listener{
		URL: &url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(hostname(opts.Hostname), strconv.Itoa(addr.Port)),
		},
		server: server,
	}, nil
}

// Close shuts down the listener.
func (listener *Listener) Close(ctx context.Context) error {
	return listener.server.Shutdown(ctx)
}

// NewHandler builds the sidecar HTTP handler.
func NewHandler(opts Options) http.Handler {
	if opts.Sessions == nil {
		opts.Sessions = storage.NewMemorySessionStore()
	}
	if opts.Messages == nil {
		if messages, ok := opts.Sessions.(session.MessageRepository); ok {
			opts.Messages = messages
		} else {
			opts.Messages = storage.NewMemorySessionStore()
		}
	}
	if opts.Runtime == nil {
		opts.Runtime = runtime.NewPromptRuntime(opts.Messages)
	}
	if opts.MCP == nil {
		opts.MCP = integration.NewMCPManager()
	}
	if opts.PTY == nil {
		opts.PTY = integration.NewPTYManager()
	}
	if opts.Interact == nil {
		opts.Interact = integration.NewInteractionManager()
	}
	if opts.Workspace == nil {
		opts.Workspace = integration.NewWorkspaceStore()
	}
	if opts.Events == nil {
		opts.Events = newEventBus()
	}
	mux := http.NewServeMux()
	health := handleJSON(func(_ *http.Request) (any, int, error) {
		return map[string]any{
			"ok":      true,
			"service": "opencode-go",
			"version": opts.Version,
		}, http.StatusOK, nil
	})
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/global/health", health)
	mux.HandleFunc("/openapi.json", handleJSON(func(_ *http.Request) (any, int, error) {
		return OpenAPI(opts.Version), http.StatusOK, nil
	}))
	mux.HandleFunc("/api/session/", v2SessionByID(opts.Sessions, opts.Messages, opts.Runtime, opts.Events))
	mux.HandleFunc("/api/session", v2Sessions(opts.Sessions))
	mux.HandleFunc("/api/provider/", v2ProviderByID())
	mux.HandleFunc("/api/provider", v2Providers())
	mux.HandleFunc("/api/model", v2Models())
	mux.HandleFunc("/event", handleEvent(opts.Version, opts.Events))
	mux.HandleFunc("/config", configGet())
	mux.HandleFunc("/instance/dispose", instanceDispose())
	mux.HandleFunc("/session/status", handleJSON(func(_ *http.Request) (any, int, error) {
		return map[string]any{}, http.StatusOK, nil
	}))
	mux.HandleFunc("/session/", sessionByID(opts.Sessions, opts.Messages, opts.Runtime, opts.Events))
	mux.HandleFunc("/session", sessions(opts.Sessions, opts.Events))
	mux.HandleFunc("/command", commandList())
	mux.HandleFunc("/config/providers", configProviders())
	mux.HandleFunc("/provider", providersList())
	mux.HandleFunc("/find/file", findFile())
	mux.HandleFunc("/find/symbol", findSymbol())
	mux.HandleFunc("/find", findText())
	mux.HandleFunc("/file/content", fileContent())
	mux.HandleFunc("/file/status", fileStatus())
	mux.HandleFunc("/file", fileList())
	mux.HandleFunc("/path", instancePath())
	mux.HandleFunc("/vcs/status", vcsStatus())
	mux.HandleFunc("/vcs/diff/raw", vcsDiffRaw())
	mux.HandleFunc("/vcs/diff", vcsDiff())
	mux.HandleFunc("/vcs/apply", vcsApply())
	mux.HandleFunc("/vcs", vcsInfo())
	mux.HandleFunc("/agent", agentsList())
	mux.HandleFunc("/skill", skillsList())
	mux.HandleFunc("/formatter", formatterStatus())
	mux.HandleFunc("/lsp", lspStatus())
	mux.HandleFunc("/project/current", projectCurrent())
	mux.HandleFunc("/project/git/init", projectInitGit(opts.Workspace))
	mux.HandleFunc("/project/", projectByID(opts.Workspace))
	mux.HandleFunc("/project", projectList(opts.Workspace))
	mux.HandleFunc("/experimental/workspace/adapter", workspaceAdapters(opts.Workspace))
	mux.HandleFunc("/experimental/workspace/sync-list", workspaceSyncList(opts.Workspace))
	mux.HandleFunc("/experimental/workspace/status", workspaceStatus(opts.Workspace))
	mux.HandleFunc("/experimental/workspace/warp", workspaceWarp(opts.Workspace))
	mux.HandleFunc("/experimental/workspace/", workspaceByID(opts.Workspace))
	mux.HandleFunc("/experimental/workspace", workspaceRoot(opts.Workspace))
	mux.HandleFunc("/question/", questionByID(opts.Interact, opts.Events))
	mux.HandleFunc("/question", questions(opts.Interact))
	mux.HandleFunc("/permission/", permissionByID(opts.Interact, opts.Events))
	mux.HandleFunc("/permission", permissions(opts.Interact))
	mux.HandleFunc("/mcp/", mcpByName(opts.MCP))
	mux.HandleFunc("/mcp", mcpRoot(opts.MCP))
	mux.HandleFunc("/pty/shells", handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return integration.Shells(), http.StatusOK, nil
	}))
	mux.HandleFunc("/pty/", ptyByID(opts.PTY))
	mux.HandleFunc("/pty", ptyRoot(opts.PTY))
	mux.HandleFunc("/tool/", toolByName(opts.Events))
	mux.HandleFunc("/tool", tools())
	return withCommonHeaders(mux)
}

// OpenAPI returns the currently migrated public HTTP contract.
func OpenAPI(version string) map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "OpenCode Go Sidecar",
			"version":     version,
			"description": "Go migration contract for local CLI, TUI, and Electron clients.",
		},
		"paths": map[string]any{
			"/health": map[string]any{
				"get": map[string]any{"operationId": "health.check"},
			},
			"/event": map[string]any{
				"get": map[string]any{"operationId": "event.subscribe"},
			},
			"/api/session": map[string]any{
				"get": map[string]any{"operationId": "v2.session.list"},
			},
			"/api/session/{sessionID}/message": map[string]any{
				"get": map[string]any{"operationId": "v2.session.messages"},
			},
			"/api/session/{sessionID}/prompt": map[string]any{
				"post": map[string]any{"operationId": "v2.session.prompt"},
			},
			"/api/session/{sessionID}/compact": map[string]any{
				"post": map[string]any{"operationId": "v2.session.compact"},
			},
			"/api/session/{sessionID}/wait": map[string]any{
				"post": map[string]any{"operationId": "v2.session.wait"},
			},
			"/api/session/{sessionID}/context": map[string]any{
				"get": map[string]any{"operationId": "v2.session.context"},
			},
			"/api/provider": map[string]any{
				"get": map[string]any{"operationId": "v2.provider.list"},
			},
			"/api/provider/{providerID}": map[string]any{
				"get": map[string]any{"operationId": "v2.provider.get"},
			},
			"/api/model": map[string]any{
				"get": map[string]any{"operationId": "v2.model.list"},
			},
			"/instance/dispose": map[string]any{
				"post": map[string]any{"operationId": "instance.dispose"},
			},
			"/path": map[string]any{
				"get": map[string]any{"operationId": "path.get"},
			},
			"/vcs": map[string]any{
				"get": map[string]any{"operationId": "vcs.get"},
			},
			"/vcs/status": map[string]any{
				"get": map[string]any{"operationId": "vcs.status"},
			},
			"/vcs/diff": map[string]any{
				"get": map[string]any{"operationId": "vcs.diff"},
			},
			"/vcs/diff/raw": map[string]any{
				"get": map[string]any{"operationId": "vcs.diff.raw"},
			},
			"/vcs/apply": map[string]any{
				"post": map[string]any{"operationId": "vcs.apply"},
			},
			"/provider": map[string]any{
				"get": map[string]any{"operationId": "provider.list"},
			},
			"/command": map[string]any{
				"get": map[string]any{"operationId": "command.list"},
			},
			"/agent": map[string]any{
				"get": map[string]any{"operationId": "app.agents"},
			},
			"/skill": map[string]any{
				"get": map[string]any{"operationId": "app.skills"},
			},
			"/formatter": map[string]any{
				"get": map[string]any{"operationId": "formatter.status"},
			},
			"/lsp": map[string]any{
				"get": map[string]any{"operationId": "lsp.status"},
			},
			"/project/current": map[string]any{
				"get": map[string]any{"operationId": "project.current"},
			},
			"/project/git/init": map[string]any{
				"post": map[string]any{"operationId": "project.initGit"},
			},
			"/project": map[string]any{
				"get": map[string]any{"operationId": "project.list"},
			},
			"/project/{projectID}": map[string]any{
				"patch": map[string]any{"operationId": "project.update"},
			},
			"/experimental/workspace/adapter": map[string]any{
				"get": map[string]any{"operationId": "experimental.workspace.adapter.list"},
			},
			"/experimental/workspace": map[string]any{
				"get":  map[string]any{"operationId": "experimental.workspace.list"},
				"post": map[string]any{"operationId": "experimental.workspace.create"},
			},
			"/experimental/workspace/sync-list": map[string]any{
				"post": map[string]any{"operationId": "experimental.workspace.syncList"},
			},
			"/experimental/workspace/status": map[string]any{
				"get": map[string]any{"operationId": "experimental.workspace.status"},
			},
			"/experimental/workspace/warp": map[string]any{
				"post": map[string]any{"operationId": "experimental.workspace.warp"},
			},
			"/experimental/workspace/{workspaceID}": map[string]any{
				"delete": map[string]any{"operationId": "experimental.workspace.remove"},
			},
			"/question": map[string]any{
				"get": map[string]any{"operationId": "question.list"},
			},
			"/question/{requestID}/reply": map[string]any{
				"post": map[string]any{"operationId": "question.reply"},
			},
			"/question/{requestID}/reject": map[string]any{
				"post": map[string]any{"operationId": "question.reject"},
			},
			"/permission": map[string]any{
				"get": map[string]any{"operationId": "permission.list"},
			},
			"/permission/{requestID}/reply": map[string]any{
				"post": map[string]any{"operationId": "permission.reply"},
			},
			"/find": map[string]any{
				"get": map[string]any{"operationId": "find.text"},
			},
			"/find/file": map[string]any{
				"get": map[string]any{"operationId": "find.files"},
			},
			"/find/symbol": map[string]any{
				"get": map[string]any{"operationId": "find.symbols"},
			},
			"/file": map[string]any{
				"get": map[string]any{"operationId": "file.list"},
			},
			"/file/content": map[string]any{
				"get": map[string]any{"operationId": "file.read"},
			},
			"/file/status": map[string]any{
				"get": map[string]any{"operationId": "file.status"},
			},
			"/config": map[string]any{
				"get": map[string]any{"operationId": "config.get"},
			},
			"/config/providers": map[string]any{
				"get": map[string]any{"operationId": "config.providers"},
			},
			"/tool": map[string]any{
				"get": map[string]any{"operationId": "tool.list"},
			},
			"/tool/{name}": map[string]any{
				"post": map[string]any{"operationId": "tool.execute"},
			},
			"/mcp": map[string]any{
				"get":  map[string]any{"operationId": "mcp.status"},
				"post": map[string]any{"operationId": "mcp.add"},
			},
			"/mcp/{name}/connect": map[string]any{
				"post": map[string]any{"operationId": "mcp.connect"},
			},
			"/mcp/{name}/disconnect": map[string]any{
				"post": map[string]any{"operationId": "mcp.disconnect"},
			},
			"/mcp/{name}/tool": map[string]any{
				"get": map[string]any{"operationId": "mcp.tools"},
			},
			"/mcp/{name}/tool/{tool}": map[string]any{
				"post": map[string]any{"operationId": "mcp.callTool"},
			},
			"/pty/shells": map[string]any{
				"get": map[string]any{"operationId": "pty.shells"},
			},
			"/pty": map[string]any{
				"get":  map[string]any{"operationId": "pty.list"},
				"post": map[string]any{"operationId": "pty.create"},
			},
			"/pty/{ptyID}": map[string]any{
				"get":    map[string]any{"operationId": "pty.get"},
				"put":    map[string]any{"operationId": "pty.update"},
				"delete": map[string]any{"operationId": "pty.remove"},
			},
			"/pty/{ptyID}/input": map[string]any{
				"post": map[string]any{"operationId": "pty.input"},
			},
			"/pty/{ptyID}/buffer": map[string]any{
				"get": map[string]any{"operationId": "pty.buffer"},
			},
			"/session": map[string]any{
				"get":  map[string]any{"operationId": "session.list"},
				"post": map[string]any{"operationId": "session.create"},
			},
			"/session/status": map[string]any{
				"get": map[string]any{"operationId": "session.status"},
			},
			"/session/{sessionID}": map[string]any{
				"get":    map[string]any{"operationId": "session.get"},
				"patch":  map[string]any{"operationId": "session.update"},
				"delete": map[string]any{"operationId": "session.delete"},
			},
			"/session/{sessionID}/children": map[string]any{
				"get": map[string]any{"operationId": "session.children"},
			},
			"/session/{sessionID}/todo": map[string]any{
				"get": map[string]any{"operationId": "session.todo"},
			},
			"/session/{sessionID}/diff": map[string]any{
				"get": map[string]any{"operationId": "session.diff"},
			},
			"/session/{sessionID}/fork": map[string]any{
				"post": map[string]any{"operationId": "session.fork"},
			},
			"/session/{sessionID}/abort": map[string]any{
				"post": map[string]any{"operationId": "session.abort"},
			},
			"/session/{sessionID}/share": map[string]any{
				"post":   map[string]any{"operationId": "session.share"},
				"delete": map[string]any{"operationId": "session.unshare"},
			},
			"/session/{sessionID}/revert": map[string]any{
				"post": map[string]any{"operationId": "session.revert"},
			},
			"/session/{sessionID}/unrevert": map[string]any{
				"post": map[string]any{"operationId": "session.unrevert"},
			},
			"/session/{sessionID}/message": map[string]any{
				"get":  map[string]any{"operationId": "session.messages"},
				"post": map[string]any{"operationId": "session.prompt"},
			},
			"/session/{sessionID}/command": map[string]any{
				"post": map[string]any{"operationId": "session.command"},
			},
			"/session/{sessionID}/message/{messageID}": map[string]any{
				"get":    map[string]any{"operationId": "session.message"},
				"delete": map[string]any{"operationId": "session.deleteMessage"},
			},
			"/session/{sessionID}/message/{messageID}/part/{partID}": map[string]any{
				"patch":  map[string]any{"operationId": "part.update"},
				"delete": map[string]any{"operationId": "part.delete"},
			},
		},
		"x-opencode-go-migration": map[string]any{
			"source": "packages/opencode/src/server/routes/instance/httpapi",
			"phase":  "go-foundation",
		},
	}
}

func configGet() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := config.Load(config.LoadOptions{
			Directory: defaultString(r.URL.Query().Get("directory"), "."),
			Worktree:  r.URL.Query().Get("worktree"),
		})
		return result, statusFromError(err), err
	})
}

func configProviders() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		cfg, err := loadRequestConfig(r)
		if err != nil {
			return nil, statusFromError(err), err
		}
		return llm.ConfigProviders(cfg.Info), http.StatusOK, nil
	})
}

func providersList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		cfg, err := loadRequestConfig(r)
		if err != nil {
			return nil, statusFromError(err), err
		}
		return llm.ListProviders(cfg.Info), http.StatusOK, nil
	})
}

func loadRequestConfig(r *http.Request) (config.LoadResult, error) {
	directory := defaultString(r.URL.Query().Get("directory"), r.Header.Get("x-opencode-directory"))
	return config.Load(config.LoadOptions{
		Directory: defaultString(directory, "."),
		Worktree:  r.URL.Query().Get("worktree"),
	})
}

func tools() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return integration.AllTools(), http.StatusOK, nil
	})
}

func toolByName(events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		name := strings.TrimPrefix(r.URL.Path, "/tool/")
		if name == "" || strings.Contains(name, "/") {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid tool name %q", name)
		}

		var request integration.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, http.StatusBadRequest, err
		}
		request.Name = name
		if request.Params == nil {
			request.Params = map[string]any{}
		}
		result, err := integration.Execute(r.Context(), request)
		if err != nil {
			return nil, integration.HTTPStatus(err), err
		}
		events.publish("tool.executed", map[string]any{
			"tool":   name,
			"result": result,
		})
		return result, http.StatusOK, nil
	})
}

func sessions(repo session.Repository, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			limit, err := parseLimit(r.URL.Query().Get("limit"))
			if err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := repo.List(r.Context(), session.ListFilter{
				Search: r.URL.Query().Get("search"),
				Limit:  limit,
			})
			return result, http.StatusOK, err
		case http.MethodPost:
			var input session.CreateInput
			if r.ContentLength != 0 {
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					return nil, http.StatusBadRequest, err
				}
			}
			result, err := repo.Create(r.Context(), input)
			if err == nil {
				events.publish("session.created", map[string]any{
					"sessionID": result.ID,
					"info":      result,
				})
			}
			return result, http.StatusOK, err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func sessionByID(repo session.Repository, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		id, remainder, err := parseSessionPath(r.URL.Path)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if remainder != "" {
			return sessionSubresource(r, id, remainder, messages, promptRuntime, events)
		}

		switch r.Method {
		case http.MethodGet:
			result, err := repo.Get(r.Context(), id)
			return result, statusFromError(err), err
		case http.MethodPatch:
			var input session.UpdateInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := repo.Update(r.Context(), id, input)
			if err == nil {
				events.publish("session.updated", map[string]any{
					"sessionID": id,
					"info":      result,
				})
			}
			return result, statusFromError(err), err
		case http.MethodDelete:
			info, _ := repo.Get(r.Context(), id)
			if err := repo.Remove(r.Context(), id); err != nil {
				return nil, statusFromError(err), err
			}
			events.publish("session.deleted", map[string]any{
				"sessionID": id,
				"info":      info,
			})
			return true, http.StatusOK, nil
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func sessionSubresource(r *http.Request, sessionID session.ID, path string, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime, events *eventBus) (any, int, error) {
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] == "command" {
		switch r.Method {
		case http.MethodPost:
			var input commandPayload
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := executeSessionCommand(r.Context(), sessionID, input, messages, promptRuntime)
			if err == nil {
				publishMessageEvents(events, sessionID, result)
			}
			return result, statusFromError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	if len(parts) == 1 && parts[0] == "children" {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		repo, ok := messages.(session.Repository)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("session repository does not support children")
		}
		result, err := repo.Children(r.Context(), sessionID)
		return result, statusFromError(err), err
	}
	if len(parts) == 1 && parts[0] == "fork" {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		repo, ok := messages.(session.Repository)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("session repository does not support fork")
		}
		var payload struct {
			MessageID *session.MessageID `json:"messageID,omitempty"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
		}
		result, err := repo.Fork(r.Context(), sessionID, payload.MessageID)
		if err == nil {
			events.publish("session.created", map[string]any{
				"sessionID": result.ID,
				"info":      result,
			})
		}
		return result, statusFromError(err), err
	}
	if len(parts) == 1 && parts[0] == "abort" {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return true, http.StatusOK, nil
	}
	if len(parts) == 1 && parts[0] == "todo" {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return []any{}, http.StatusOK, nil
	}
	if len(parts) == 1 && parts[0] == "diff" {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return []any{}, http.StatusOK, nil
	}
	if len(parts) == 1 && parts[0] == "share" {
		repo, ok := messages.(session.Repository)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("session repository does not support share")
		}
		switch r.Method {
		case http.MethodPost:
			info, err := repo.Update(r.Context(), sessionID, session.UpdateInput{
				Share: &session.ShareInfo{URL: fmt.Sprintf("opencode://session/%s", sessionID)},
			})
			return info, statusFromError(err), err
		case http.MethodDelete:
			info, err := repo.Update(r.Context(), sessionID, session.UpdateInput{ClearShare: true})
			return info, statusFromError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	if len(parts) == 1 && parts[0] == "revert" {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		repo, ok := messages.(session.Repository)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("session repository does not support revert")
		}
		var payload struct {
			MessageID session.MessageID `json:"messageID"`
			PartID    *session.PartID   `json:"partID,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		if payload.MessageID == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("messageID is required")
		}
		info, err := repo.Update(r.Context(), sessionID, session.UpdateInput{
			Revert: &session.RevertInfo{MessageID: payload.MessageID, PartID: payload.PartID},
			Summary: &session.SummaryInfo{
				Additions: 0,
				Deletions: 0,
				Files:     0,
			},
		})
		return info, statusFromError(err), err
	}
	if len(parts) == 1 && parts[0] == "unrevert" {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		repo, ok := messages.(session.Repository)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("session repository does not support unrevert")
		}
		info, err := repo.Update(r.Context(), sessionID, session.UpdateInput{ClearRevert: true})
		return info, statusFromError(err), err
	}
	if len(parts) == 1 && parts[0] == "message" {
		switch r.Method {
		case http.MethodGet:
			limit, err := parseLimit(r.URL.Query().Get("limit"))
			if err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := messages.Messages(r.Context(), sessionID, limit)
			return result, statusFromError(err), err
		case http.MethodPost:
			var input session.PromptInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := messages.CreatePrompt(r.Context(), sessionID, input)
			if err != nil {
				return result, statusFromError(err), err
			}
			publishMessageEvents(events, sessionID, result)
			if !input.NoReply && promptRuntime != nil {
				assistant, err := promptRuntime.Reply(r.Context(), sessionID, result)
				if err != nil {
					return nil, statusFromError(err), err
				}
				publishMessageEvents(events, sessionID, assistant)
			}
			return result, http.StatusOK, nil
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	if len(parts) == 2 && parts[0] == "message" {
		messageID := session.MessageID(parts[1])
		switch r.Method {
		case http.MethodGet:
			result, err := messages.GetMessage(r.Context(), sessionID, messageID)
			return result, statusFromError(err), err
		case http.MethodDelete:
			err := messages.RemoveMessage(r.Context(), sessionID, messageID)
			if err == nil {
				events.publish("message.removed", map[string]any{
					"sessionID": sessionID,
					"messageID": messageID,
				})
			}
			return true, statusFromError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	if len(parts) == 4 && parts[0] == "message" && parts[2] == "part" {
		messageID := session.MessageID(parts[1])
		partID := session.PartID(parts[3])
		switch r.Method {
		case http.MethodDelete:
			err := messages.RemovePart(r.Context(), sessionID, messageID, partID)
			if err == nil {
				events.publish("message.part.removed", map[string]any{
					"sessionID": sessionID,
					"messageID": messageID,
					"partID":    partID,
				})
			}
			return true, statusFromError(err), err
		case http.MethodPatch:
			var part session.Part
			if err := json.NewDecoder(r.Body).Decode(&part); err != nil {
				return nil, http.StatusBadRequest, err
			}
			if part.ID != partID || part.MessageID != messageID || part.SessionID != sessionID {
				return nil, http.StatusBadRequest, fmt.Errorf("part path identifiers do not match payload")
			}
			result, err := messages.UpdatePart(r.Context(), part)
			if err == nil {
				events.publish("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"messageID": messageID,
					"part":      result,
				})
			}
			return result, statusFromError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	return nil, http.StatusNotFound, fmt.Errorf("unknown session route /session/%s/%s", sessionID, path)
}

type commandPayload struct {
	MessageID *session.MessageID `json:"messageID,omitempty"`
	Agent     string             `json:"agent,omitempty"`
	Model     string             `json:"model,omitempty"`
	Arguments string             `json:"arguments"`
	Command   string             `json:"command"`
	Variant   string             `json:"variant,omitempty"`
	Directory string             `json:"directory,omitempty"`
	NoReply   bool               `json:"noReply,omitempty"`
	Parts     []session.Part     `json:"parts,omitempty"`
}

func executeSessionCommand(ctx context.Context, sessionID session.ID, input commandPayload, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime) (session.WithParts, error) {
	rendered, err := config.ExecuteCommand(ctx, config.CommandInput{
		Name:      input.Command,
		Argument:  input.Arguments,
		Directory: defaultString(input.Directory, "."),
	})
	if err != nil {
		return session.WithParts{}, err
	}
	providerID, modelID := parseProviderModel(rendered.Provider, rendered.Model, input.Model)
	parts := []session.Part{{
		Type: "text",
		Data: map[string]any{
			"text": rendered.Prompt,
			"metadata": map[string]any{
				"command":   rendered.Command.Name,
				"arguments": rendered.Argument,
			},
		},
	}}
	parts = append(parts, input.Parts...)
	user, err := messages.CreatePrompt(ctx, sessionID, session.PromptInput{
		MessageID: input.MessageID,
		Agent:     defaultString(rendered.Agent, defaultString(input.Agent, "build")),
		Model:     &session.ModelRef{ProviderID: providerID, ModelID: modelID, Variant: input.Variant},
		NoReply:   input.NoReply,
		Parts:     parts,
	})
	if err != nil {
		return session.WithParts{}, err
	}
	if input.NoReply || promptRuntime == nil {
		return user, nil
	}
	return promptRuntime.Reply(ctx, sessionID, user)
}

func publishMessageEvents(events *eventBus, sessionID session.ID, message session.WithParts) {
	events.publish("message.updated", map[string]any{
		"sessionID": sessionID,
		"info":      message,
	})
	for _, part := range message.Parts {
		events.publish("message.part.updated", map[string]any{
			"sessionID": sessionID,
			"messageID": message.Info.ID,
			"part":      part,
		})
	}
}

func parseProviderModel(commandProvider string, commandModel string, fallback string) (string, string) {
	if commandProvider != "" && commandModel != "" {
		return commandProvider, commandModel
	}
	model := fallback
	if model == "" && commandModel != "" {
		model = commandModel
	}
	if model == "" {
		return "openai-compatible", "gpt-4o-mini"
	}
	provider, modelID, ok := strings.Cut(model, "/")
	if !ok {
		return "openai-compatible", model
	}
	return provider, modelID
}

func parseSessionPath(path string) (session.ID, string, error) {
	rest := strings.TrimPrefix(path, "/session/")
	if rest == path || rest == "" {
		return "", "", fmt.Errorf("invalid session path %q", path)
	}
	parts := strings.SplitN(rest, "/", 2)
	id, err := session.ParseID(parts[0])
	if err != nil {
		return "", "", err
	}
	if len(parts) == 1 {
		return id, "", nil
	}
	return id, parts[1], nil
}

func handleEvent(version string, events *eventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)
		if err := writeSSE(w, event{
			ID:   "evt_ready",
			Type: "server.connected",
			Properties: map[string]any{
				"version": version,
			},
		}); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		ch, unsubscribe := events.subscribe(r.Context())
		defer unsubscribe()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case item, ok := <-ch:
				if !ok {
					return
				}
				if err := writeSSE(w, item); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			case <-ticker.C:
				if err := writeSSE(w, event{ID: "evt_heartbeat", Type: "server.heartbeat", Properties: map[string]any{}}); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
}

func writeSSE(w io.Writer, item event) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if item.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", item.ID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "event: message\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

func handleJSON(fn func(*http.Request) (any, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, status, err := fn(r)
		if err != nil {
			writeError(w, status, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			return
		}
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encodeErr := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": err.Error(),
		},
	}); encodeErr != nil {
		return
	}
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseLimit(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("invalid limit %q", value)
	}
	return limit, nil
}

func statusFromError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, session.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func listenTCP(host string, port int) (net.Listener, error) {
	host = hostname(host)
	if port != 0 {
		return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "4096"))
	if err == nil {
		return listener, nil
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

func hostname(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
