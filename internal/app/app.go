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
	"slices"
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
	case "run":
		return runPrompt(ctx, args[1:], stdout, stderr)
	case "session":
		return sessionCommand(ctx, args[1:], stdout, stderr)
	case "retry-delay":
		return retryDelay(args[1:], stdout, stderr)
	case "commands":
		return commands(args[1:], stdout, stderr)
	case "config":
		return configCommand(args[1:], stdout, stderr)
	case "providers":
		return providers(args[1:], stdout, stderr)
	case "models":
		return models(args[1:], stdout, stderr)
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

func runPrompt(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; empty uses in-memory storage")
	text := fs.String("text", "", "text prompt to run")
	textFile := fs.String("text-file", "", "file containing prompt text, or - for stdin")
	title := fs.String("title", "", "session title")
	agent := fs.String("agent", "build", "agent name")
	provider := fs.String("provider", "", "provider id")
	model := fs.String("model", "", "model id")
	jsonOutput := fs.Bool("json", false, "write full assistant message JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode run [--db PATH] [--text TEXT | --text-file PATH] [--json] [PROMPT]")
		return 2
	}
	if *text != "" && *textFile != "" {
		_, _ = fmt.Fprintln(stderr, "--text and --text-file are mutually exclusive")
		return 2
	}
	promptText := *text
	if promptText == "" && *textFile == "" && fs.NArg() == 1 {
		promptText = fs.Arg(0)
	}
	var err error
	if *textFile != "" {
		promptText, err = readPromptText("", *textFile, os.Stdin)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "read prompt text failed: %v\n", err)
			return 2
		}
	}
	if strings.TrimSpace(promptText) == "" {
		_, _ = fmt.Fprintln(stderr, "prompt text is required")
		return 2
	}

	sessionRepo, messageRepo, _, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer closeRepo()

	sessionTitle := *title
	if sessionTitle == "" {
		sessionTitle = promptTitle(promptText)
	}
	info, err := sessionRepo.Create(ctx, session.CreateInput{Title: sessionTitle})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create session failed: %v\n", err)
		return 1
	}
	input := session.PromptInput{
		Agent: *agent,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": promptText},
		}},
	}
	if *provider != "" || *model != "" {
		input.Model = &session.ModelRef{ProviderID: defaultAppString(*provider, "openai-compatible"), ModelID: defaultAppString(*model, "gpt-4o-mini")}
	}
	if err := resolveAppPromptDefaults(ctx, info.ID, &input, messageRepo, "."); err != nil {
		_, _ = fmt.Fprintf(stderr, "resolve prompt defaults failed: %v\n", err)
		return 1
	}
	user, err := messageRepo.CreatePrompt(ctx, info.ID, input)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create prompt failed: %v\n", err)
		return 1
	}
	assistant, err := runtime.NewPromptRuntime(messageRepo).Reply(ctx, info.ID, user)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create assistant reply failed: %v\n", err)
		return 1
	}
	if *jsonOutput {
		return writeJSON(stdout, assistant)
	}
	textReply := messageText(assistant)
	if textReply == "" {
		textReply = assistant.Info.Finish
	}
	if _, err := fmt.Fprintln(stdout, textReply); err != nil {
		return 1
	}
	return 0
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

	sessionRepo, messageRepo, syncStore, closeRepo, err := openRepositories(*dbPath)
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
		Sync:     syncStore,
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

func openRepositories(dbPath string) (server.SessionRepository, server.MessageRepository, *integration.SyncStore, func(), error) {
	resolved, err := resolveDBPath(dbPath)
	if err != nil {
		return nil, nil, nil, func() {}, err
	}
	if resolved == "" {
		store := storage.NewMemorySessionStore()
		return store, store, integration.NewSyncStore(), func() {}, nil
	}
	store, err := storage.OpenSQLiteSessionStore(resolved)
	if err != nil {
		return nil, nil, nil, func() {}, err
	}
	return store, store, integration.NewSyncStoreWithPersistence(store), func() {
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

	sessionRepo, messageRepo, _, closeRepo, err := openRepositories(*dbPath)
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
	case "children":
		return sessionChildren(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
	case "fork":
		return sessionFork(ctx, sessionRepo, fs.Args()[1:], stdout, stderr)
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

func sessionChildren(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session children", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] children SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	result, err := repo.Children(ctx, id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list children failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func sessionFork(ctx context.Context, repo server.SessionRepository, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("session fork", flag.ContinueOnError)
	fs.SetOutput(stderr)
	messageID := fs.String("message", "", "optional message id to fork through")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode session [--db PATH] fork [--message MESSAGE_ID] SESSION_ID")
		return 2
	}
	id, err := session.ParseID(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
		return 2
	}
	var forkMessageID *session.MessageID
	if *messageID != "" {
		parsed := session.MessageID(*messageID)
		forkMessageID = &parsed
	}
	result, err := repo.Fork(ctx, id, forkMessageID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fork session failed: %v\n", err)
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
	provider := fs.String("provider", "", "provider id")
	model := fs.String("model", "", "model id")
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
	input := session.PromptInput{
		Agent:   *agent,
		NoReply: *noReply,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": promptText},
		}},
	}
	if *provider != "" || *model != "" {
		input.Model = &session.ModelRef{ProviderID: defaultAppString(*provider, "openai-compatible"), ModelID: defaultAppString(*model, "gpt-4o-mini")}
	}
	if err := resolveAppPromptDefaults(ctx, id, &input, repo, "."); err != nil {
		_, _ = fmt.Fprintf(stderr, "resolve prompt defaults failed: %v\n", err)
		return 1
	}
	result, err := repo.CreatePrompt(ctx, id, input)
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
	if providerID == "" && modelID == "" && *model != "" {
		providerID, modelID = parseProviderModel(*model)
	}
	input := session.PromptInput{
		Agent:   agentName,
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
	}
	if providerID != "" || modelID != "" {
		input.Model = &session.ModelRef{ProviderID: providerID, ModelID: modelID}
	}
	if err := resolveAppPromptDefaults(ctx, id, &input, repo, *directory); err != nil {
		_, _ = fmt.Fprintf(stderr, "resolve command prompt defaults failed: %v\n", err)
		return 1
	}
	result, err := repo.CreatePrompt(ctx, id, input)
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

func promptTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "New session"
	}
	if len(text) <= 60 {
		return text
	}
	return text[:60]
}

func messageText(message session.WithParts) string {
	parts := []string{}
	for _, part := range message.Parts {
		if part.Type != "text" {
			continue
		}
		if text, ok := part.Data["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func resolveAppPromptDefaults(ctx context.Context, sessionID session.ID, input *session.PromptInput, repo server.MessageRepository, directory string) error {
	loaded, err := config.Load(config.LoadOptions{Directory: defaultAppString(directory, ".")})
	if err != nil {
		return err
	}
	return runtime.ResolvePromptDefaults(ctx, sessionID, input, repo, loaded.Info)
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
	jsonOutput := fs.Bool("json", false, "write provider list JSON")
	directory := fs.String("directory", ".", "directory used to load provider config")
	worktree := fs.String("worktree", "", "worktree boundary for provider config discovery")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *jsonOutput {
		cfg, err := config.Load(config.LoadOptions{
			Directory: *directory,
			Worktree:  *worktree,
		})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "load provider config failed: %v\n", err)
			return 1
		}
		return writeJSON(stdout, llm.ListProviders(cfg.Info))
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(llm.ProviderIDs(), "\n")); err != nil {
		return 1
	}
	return 0
}

func models(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("verbose", false, "include model metadata JSON after each model id")
	refresh := fs.Bool("refresh", false, "accepted for TypeScript CLI compatibility; Go reads current local provider metadata")
	directory := fs.String("directory", ".", "directory used to load provider config")
	worktree := fs.String("worktree", "", "worktree boundary for provider config discovery")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode models [--verbose] [--refresh] [--directory DIR] [--worktree DIR] [PROVIDER]")
		return 2
	}
	if *refresh {
		_, _ = fmt.Fprintln(stderr, "warning: --refresh is not needed in the Go provider registry")
	}
	cfg, err := config.Load(config.LoadOptions{
		Directory: *directory,
		Worktree:  *worktree,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load provider config failed: %v\n", err)
		return 1
	}
	list := llm.ListProviders(cfg.Info)
	providerFilter := ""
	if fs.NArg() == 1 {
		providerFilter = fs.Arg(0)
	}
	for _, provider := range list.All {
		if providerFilter != "" && provider.ID != providerFilter {
			continue
		}
		for _, modelID := range sortedModelIDs(provider.Models) {
			if _, err := fmt.Fprintf(stdout, "%s/%s\n", provider.ID, modelID); err != nil {
				return 1
			}
			if *verbose {
				if err := writeJSON(stdout, provider.Models[modelID]); err != 0 {
					return err
				}
			}
		}
		if providerFilter != "" {
			return 0
		}
	}
	if providerFilter != "" {
		_, _ = fmt.Fprintf(stderr, "provider not found: %s\n", providerFilter)
		return 2
	}
	return 0
}

func sortedModelIDs(models map[string]llm.PublicModel) []string {
	result := make([]string, 0, len(models))
	for id := range models {
		result = append(result, id)
	}
	slices.Sort(result)
	return result
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

func configCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover project config")
	worktree := fs.String("worktree", "", "worktree boundary for project config discovery")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode config [--directory DIR] [--worktree DIR]")
		return 2
	}
	result, err := config.Load(config.LoadOptions{
		Directory: *directory,
		Worktree:  *worktree,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config failed: %v\n", err)
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
  run [--db PATH] [--text TEXT | --text-file PATH] [--json] [PROMPT]
  session [--db PATH] COMMAND
  config [--directory DIR] [--worktree DIR]
  commands [--directory DIR]
  providers [--json] [--directory DIR] [--worktree DIR]
  models [--verbose] [--refresh] [--directory DIR] [--worktree DIR] [PROVIDER]
  tools
  tool [--directory DIR] [--params JSON | --params-file PATH] NAME
  retry-delay --attempt N [--retry-after-ms MS | --retry-after VALUE]

provider env:
  OPENCODE_OPENAI_COMPATIBLE_BASE_URL, OPENCODE_OPENAI_COMPATIBLE_API_KEY, OPENCODE_OPENAI_COMPATIBLE_MODEL

storage env:
  OPENCODE_DB`)
	return err
}
