// Package server contains the Go headless HTTP sidecar.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/runtime"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

// Options configures the Go sidecar listener.
type Options struct {
	Hostname string
	Port     int
	Version  string
	Sessions session.Repository
	Messages session.MessageRepository
	Runtime  *runtime.PromptRuntime
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
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleJSON(func(_ *http.Request) (any, int, error) {
		return map[string]any{
			"ok":      true,
			"service": "opencode-go",
			"version": opts.Version,
		}, http.StatusOK, nil
	}))
	mux.HandleFunc("/openapi.json", handleJSON(func(_ *http.Request) (any, int, error) {
		return OpenAPI(opts.Version), http.StatusOK, nil
	}))
	mux.HandleFunc("/event", handleEvent(opts.Version))
	mux.HandleFunc("/session/status", handleJSON(func(_ *http.Request) (any, int, error) {
		return map[string]any{}, http.StatusOK, nil
	}))
	mux.HandleFunc("/session/", sessionByID(opts.Sessions, opts.Messages, opts.Runtime))
	mux.HandleFunc("/session", sessions(opts.Sessions))
	mux.HandleFunc("/provider", handleJSON(func(_ *http.Request) (any, int, error) {
		return llm.AllProviders(), http.StatusOK, nil
	}))
	mux.HandleFunc("/tool/", toolByName())
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
			"/provider": map[string]any{
				"get": map[string]any{"operationId": "provider.list"},
			},
			"/tool": map[string]any{
				"get": map[string]any{"operationId": "tool.list"},
			},
			"/tool/{name}": map[string]any{
				"post": map[string]any{"operationId": "tool.execute"},
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
			"/session/{sessionID}/message": map[string]any{
				"get":  map[string]any{"operationId": "session.messages"},
				"post": map[string]any{"operationId": "session.prompt"},
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

func tools() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return integration.AllTools(), http.StatusOK, nil
	})
}

func toolByName() http.HandlerFunc {
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
		return result, http.StatusOK, nil
	})
}

func sessions(repo session.Repository) http.HandlerFunc {
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
			return result, http.StatusOK, err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func sessionByID(repo session.Repository, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		id, remainder, err := parseSessionPath(r.URL.Path)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if remainder != "" {
			return sessionSubresource(r, id, remainder, messages, promptRuntime)
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
			return result, statusFromError(err), err
		case http.MethodDelete:
			if err := repo.Remove(r.Context(), id); err != nil {
				return nil, statusFromError(err), err
			}
			return true, http.StatusOK, nil
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func sessionSubresource(r *http.Request, sessionID session.ID, path string, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime) (any, int, error) {
	parts := strings.Split(path, "/")
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
			if !input.NoReply && promptRuntime != nil {
				if _, err := promptRuntime.Reply(r.Context(), sessionID, result); err != nil {
					return nil, statusFromError(err), err
				}
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
			return result, statusFromError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	}
	return nil, http.StatusNotFound, fmt.Errorf("unknown session route /session/%s/%s", sessionID, path)
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

func handleEvent(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		event := map[string]any{
			"type": "server.ready",
			"properties": map[string]any{
				"version": version,
			},
		}
		payload, err := json.Marshal(event)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := fmt.Fprint(w, "event: ready\n"); err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return
		}
	}
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
