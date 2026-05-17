// Package app owns the Go command-line surface while migration moves behavior
// out of the legacy TypeScript package.
package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/config"
	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/domain/session/retry"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/runtime"
	"github.com/RecoveryAshes/opencode/internal/server"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

// Run executes the opencode command and returns the intended process exit code.
func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, version string) int {
	if len(args) == 0 {
		if err := printUsage(stderr); err != nil {
			return 1
		}
		return 0
	}

	switch args[0] {
	case "help", "--help", "-h":
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	case "version", "--version", "-v":
		if _, err := fmt.Fprintln(stdout, version); err != nil {
			return 1
		}
		return 0
	case "serve":
		return serve(ctx, args[1:], stdout, stderr, version)
	case "session":
		return sessionCommand(ctx, args[1:], stdout, stderr)
	case "retry-delay":
		return retryDelay(args[1:], stdout, stderr)
	case "commands":
		return commands(args[1:], stdout, stderr)
	case "providers":
		return providers(args[1:], stdout, stderr)
	case "tools":
		return tools(args[1:], stdout, stderr)
	case "tool":
		return tool(ctx, args[1:], stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0]); err != nil {
			return 1
		}
		if err := printUsage(stderr); err != nil {
			return 1
		}
		return 2
	}
}

func serve(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, version string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hostname := fs.String("hostname", "127.0.0.1", "hostname to bind")
	port := fs.Int("port", 0, "port to bind; 0 prefers 4096 then any free port")
	dbPath := fs.String("db", "", "SQLite database path; empty uses in-memory storage")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sessionRepo, messageRepo, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "open db failed: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	defer closeRepo()

	listener, err := server.Listen(ctx, server.Options{
		Hostname: *hostname,
		Port:     *port,
		Version:  version,
		Sessions: sessionRepo,
		Messages: messageRepo,
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "serve failed: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	defer func() {
		if err := listener.Close(context.Background()); err != nil {
			_, _ = fmt.Fprintf(stderr, "shutdown failed: %v\n", err)
		}
	}()

	if _, err := fmt.Fprintln(stdout, listener.URL.String()); err != nil {
		return 1
	}
	<-ctx.Done()
	return 0
}

func openRepositories(dbPath string) (server.SessionRepository, server.MessageRepository, func(), error) {
	resolved, err := resolveDBPath(dbPath)
	if err != nil {
		return nil, nil, func() {}, err
	}
	if resolved == "" {
		store := storage.NewMemorySessionStore()
		return store, store, func() {}, nil
	}
	store, err := storage.OpenSQLiteSessionStore(resolved)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return store, store, func() {
		_ = store.Close()
	}, nil
}

func resolveDBPath(dbPath string) (string, error) {
	if dbPath == "" {
		dbPath = os.Getenv("OPENCODE_DB")
	}
	if dbPath == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return "", fmt.Errorf("create db directory: %w", err)
	}
	return dbPath, nil
}

func sessionCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; empty uses in-memory storage")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] COMMAND")
		return 2
	}

	sessionRepo, messageRepo, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer closeRepo()

	switch fs.Arg(0) {
	case "list":
		return sessionList(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "create":
		return sessionCreate(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "get":
		return sessionGet(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "update":
		return sessionUpdate(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "delete", "remove":
		return sessionDelete(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "messages":
		return sessionMessages(ctx, messageRepo, fs.Args()[1:], stdout, stderr)
	case "prompt":
		return sessionPrompt(ctx, messageRepo, fs.Args()[1:], stdout, stderr)
	case "command":
		return sessionRunCommand(ctx, messageRepo, fs.Args()[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown session command: %s\n", fs.Arg(0))
		return 2
	}
}

func sessionList(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	search := fs.String("search", "", "filter sessions by title")
	limit := fs.Int("limit", 0, "maximum sessions to return")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] list [--search TEXT] [--limit N]")
		return 2
	}
	result, err := repo.List(ctx, session.ListFilter{Search: *search, Limit: *limit})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list sessions failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionCreate(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	title := fs.String("title", "", "session title")
	parent := fs.String("parent", "", "parent session id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] create [--title TITLE] [--parent SESSION_ID]")
		return 2
	}
	input := session.CreateInput{Title: *title}
	if *parent != "" {
		parentID, err := session.ParseID(*parent)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "invalid parent id: %v\n", err)
			return 2
		}
		input.ParentID = &parentID
	}
	result, err := repo.Create(ctx, input)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create session failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionGet(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] get SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	result, err := repo.Get(ctx, id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "get session failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionUpdate(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	title := fs.String("title", "", "new session title")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *title == "" {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] update --title TITLE SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	result, err := repo.Update(ctx, id, session.UpdateInput{Title: title})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "update session failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionDelete(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] delete SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	if err := repo.Remove(ctx, id); err != nil {
		_, _ = fmt.Fprintf(stderr, "delete session failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, true)
}

func sessionMessages(ctx context.Context, repo server.MessageRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session messages", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 0, "maximum messages to return")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] messages [--limit N] SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	result, err := repo.Messages(ctx, id, *limit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list session messages failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionPrompt(ctx context.Context, repo server.MessageRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session prompt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "text prompt to append")
	textFile := fs.String("text-file", "", "file containing prompt text, or - for stdin")
	agent := fs.String("agent", "build", "agent name")
	provider := fs.String("provider", "openai-compatible", "provider id")
	model := fs.String("model", "gpt-4o-mini", "model id")
	noReply := fs.Bool("no-reply", false, "store the prompt without running an assistant reply")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] prompt [--text TEXT | --text-file PATH] [--no-reply] SESSION_ID")
		return 2
	}
	if *text != "" && *textFile != "" {
		_, _ = fmt.Fprintln(stderr, "--text and --text-file are mutually exclusive")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	promptText, err := readPromptText(*text, *textFile, os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read prompt text failed: %v\n", err)
		return 2
	}
	result, err := repo.CreatePrompt(ctx, id, session.PromptInput{
		Agent:   *agent,
		Model:   &session.ModelRef{ProviderID: *provider, ModelID: *model},
		NoReply: *noReply,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": promptText},
		}},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create prompt failed: %v\n", err)
		return 1
	}
	if !*noReply {
		assistant, err := runtime.NewPromptRuntime(repo).Reply(ctx, id, result)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "create assistant reply failed: %v\n", err)
			return 1
		}
		return writeJSON(stdout, assistant)
	}
	return writeJSON(stdout, result)
}

func sessionRunCommand(ctx context.Context, repo server.MessageRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session command", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover command files")
	argument := fs.String("argument", "", "raw slash-command arguments")
	agent := fs.String("agent", "build", "fallback agent name")
	model := fs.String("model", "", "fallback provider/model id")
	noReply := fs.Bool("no-reply", false, "store the rendered command prompt without running an assistant reply")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] command [--directory DIR] [--argument TEXT] [--no-reply] SESSION_ID COMMAND")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	rendered, err := config.ExecuteCommand(ctx, config.CommandInput{
		Name:      fs.Arg(1),
		Argument:  *argument,
		Directory: *directory,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "execute command failed: %v\n", err)
		return 1
	}
	agentName := defaultAppString(rendered.Agent, *agent)
	providerID, modelID := rendered.Provider, rendered.Model
	if providerID == "" || modelID == "" {
		providerID, modelID = parseProviderModel(*model)
	}
	result, err := repo.CreatePrompt(ctx, id, session.PromptInput{
		Agent:   agentName,
		Model:   &session.ModelRef{ProviderID: providerID, ModelID: modelID},
		NoReply: *noReply,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{
				"text": rendered.Prompt,
				"metadata": map[string]any{
					"command":   rendered.Command.Name,
					"arguments": rendered.Argument,
				},
			},
		}},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create command prompt failed: %v\n", err)
		return 1
	}
	if !*noReply {
		assistant, err := runtime.NewPromptRuntime(repo).Reply(ctx, id, result)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "create assistant reply failed: %v\n", err)
			return 1
		}
		return writeJSON(stdout, assistant)
	}
	return writeJSON(stdout, result)
}

func readPromptText(text string, textFile string, stdin io.Reader) (string, error) {
	if textFile == "" {
		return text, nil
	}
	if textFile == "-" {
		data, err := io.ReadAll(stdin)
		return string(data), err
	}
	data, err := os.ReadFile(textFile)
	return string(data), err
}

func parseProviderModel(model string) (string, string) {
	if model == "" {
		return "openai-compatible", "gpt-4o-mini"
	}
	provider, modelID, ok := strings.Cut(model, "/")
	if !ok {
		return "openai-compatible", model
	}
	return provider, modelID
}

func defaultAppString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(stdout io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return 1
	}
	return 0
}

func retryDelay(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("retry-delay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	attempt := fs.Int("attempt", 1, "one-based retry attempt")
	retryAfterMs := fs.String("retry-after-ms", "", "retry-after-ms response header")
	retryAfter := fs.String("retry-after", "", "retry-after response header")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	headers := map[string]string{}
	if *retryAfterMs != "" {
		headers["retry-after-ms"] = *retryAfterMs
	}
	if *retryAfter != "" {
		headers["retry-after"] = *retryAfter
	}

	var apiErr *retry.APIError
	if len(headers) > 0 {
		apiErr = &retry.APIError{Message: "retry", IsRetryable: true, ResponseHeaders: headers}
	}

	if _, err := fmt.Fprintln(stdout, retry.Delay(*attempt, apiErr, nil)); err != nil {
		return 1
	}
	return 0
}

func providers(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(llm.ProviderIDs(), "\n")); err != nil {
		return 1
	}
	return 0
}

func tools(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(integration.ToolNames(), "\n")); err != nil {
		return 1
	}
	return 0
}

func commands(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("commands", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover command files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode commands [--directory DIR]")
		return 2
	}
	result, err := config.LoadCommands(*directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load commands failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func tool(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("tool", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "working directory for relative tool paths")
	paramsJSON := fs.String("params", "{}", "tool parameters as JSON object")
	paramsFile := fs.String("params-file", "", "path to JSON file containing tool parameters, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode tool [--directory DIR] [--params JSON | --params-file PATH] NAME")
		return 2
	}
	if *paramsFile != "" && *paramsJSON != "{}" {
		_, _ = fmt.Fprintln(stderr, "--params and --params-file are mutually exclusive")
		return 2
	}
	params, err := decodeToolParams(*paramsJSON, *paramsFile, os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "decode params failed: %v\n", err)
		return 2
	}
	result, err := integration.Execute(ctx, integration.Request{
		Name:      fs.Arg(0),
		Directory: *directory,
		Params:    params,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tool failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func decodeToolParams(paramsJSON string, paramsFile string, stdin io.Reader) (map[string]any, error) {
	if paramsFile != "" {
		var data []byte
		var err error
		if paramsFile == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(paramsFile)
		}
		if err != nil {
			return nil, err
		}
		paramsJSON = string(data)
	}
	paramsJSON = strings.TrimSpace(paramsJSON)
	if paramsJSON == "" {
		paramsJSON = "{}"
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]any{}
	}
	return params, nil
}

func printUsage(w io.Writer) error {
	_, err := fmt.Fprintln(w, `opencode

commands:
  version
  serve [--hostname HOST] [--port PORT] [--db PATH]
  session [--db PATH] COMMAND
  commands [--directory DIR]
  providers
  tools
  tool [--directory DIR] [--params JSON | --params-file PATH] NAME
  retry-delay --attempt N [--retry-after-ms MS | --retry-after VALUE]

provider env:
  OPENCODE_OPENAI_COMPATIBLE_BASE_URL, OPENCODE_OPENAI_COMPATIBLE_API_KEY, OPENCODE_OPENAI_COMPATIBLE_MODEL

storage env:
  OPENCODE_DB`)
	return err
}
