// Package app owns the Go command-line surface while migration moves behavior
// out of the legacy TypeScript package.
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	case "stats":
		return statsCommand(ctx, args[1:], stdout, stderr)
	case "retry-delay":
		return retryDelay(args[1:], stdout, stderr)
	case "commands":
		return commands(args[1:], stdout, stderr)
	case "config":
		return configCommand(args[1:], stdout, stderr)
	case "db":
		return dbCommand(ctx, args[1:], stdout, stderr)
	case "debug":
		return debugCommand(ctx, args[1:], stdout, stderr)
	case "export":
		return exportCommand(ctx, args[1:], stdout, stderr)
	case "import":
		return importCommand(ctx, args[1:], stdout, stderr)
	case "agent":
		return agentCommand(args[1:], stdout, stderr)
	case "mcp":
		return mcpCommand(ctx, args[1:], stdout, stderr)
	case "pty":
		return ptyCommand(ctx, args[1:], stdout, stderr)
	case "skills":
		return skillsCommand(args[1:], stdout, stderr)
	case "formatters":
		return formattersCommand(args[1:], stdout, stderr)
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

	mcpManager, closeMCP, err := openMCPManager(ctx, ".")
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "open mcp failed: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	defer closeMCP()

	listener, err := server.Listen(ctx, server.Options{
		Hostname: *hostname,
		Port:     *port,
		Version:  version,
		Sessions: sessionRepo,
		Messages: messageRepo,
		MCP:      mcpManager,
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

func openMCPManager(ctx context.Context, directory string) (*integration.MCPManager, func(), error) {
	manager, _, closeManager, err := openConfiguredMCPManager(ctx, directory)
	return manager, closeManager, err
}

func openConfiguredMCPManager(ctx context.Context, directory string) (*integration.MCPManager, []integration.MCPConfiguredServer, func(), error) {
	manager := integration.NewMCPManager()
	servers, err := integration.LoadConfiguredMCPServers(directory)
	if err != nil {
		return nil, nil, func() {}, err
	}
	for _, configured := range servers {
		if configured.Disabled {
			manager.Add(ctx, configured.Name, integration.MCPConfig{
				Type:    defaultAppString(configured.Config.Type, "local"),
				Enabled: boolPtr(false),
			})
			continue
		}
		manager.Add(ctx, configured.Name, configured.Config)
	}
	return manager, servers, func() {
		for _, configured := range servers {
			manager.Disconnect(configured.Name)
		}
	}, nil
}

func boolPtr(value bool) *bool {
	return &value
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

func resolvePersistentDBPath(dbPath string) (string, error) {
	if dbPath == "" {
		dbPath = os.Getenv("OPENCODE_DB")
	}
	if dbPath == "" {
		dbPath = filepath.Join(appDataDir(), "opencode.db")
	} else if dbPath != ":memory:" && !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(appDataDir(), dbPath)
	}
	if dbPath == ":memory:" {
		return dbPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return "", fmt.Errorf("create db directory: %w", err)
	}
	return dbPath, nil
}

func appDataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "opencode")
	}
	home := os.Getenv("OPENCODE_TEST_HOME")
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	return filepath.Join(home, ".local", "share", "opencode")
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

func agentCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover agent files")
	jsonOutput := fs.Bool("json", false, "write agent data JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode agent [--directory DIR] [--json] COMMAND")
		return 2
	}
	agents, err := integration.ListAgents(*directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list agents failed: %v\n", err)
		return 1
	}
	sortAgentsForCLI(agents)
	switch fs.Arg(0) {
	case "list":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode agent [--directory DIR] [--json] list")
			return 2
		}
		if *jsonOutput {
			return writeJSON(stdout, agents)
		}
		for _, agent := range agents {
			if _, err := fmt.Fprintf(stdout, "%s (%s)\n", agent.Name, agent.Mode); err != nil {
				return 1
			}
			data, err := json.MarshalIndent(agent.Permission, "  ", "  ")
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "encode agent permission failed: %v\n", err)
				return 1
			}
			if _, err := fmt.Fprintf(stdout, "  %s\n", data); err != nil {
				return 1
			}
		}
		return 0
	case "get":
		if fs.NArg() != 2 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode agent [--directory DIR] [--json] get NAME")
			return 2
		}
		name := fs.Arg(1)
		for _, agent := range agents {
			if agent.Name != name {
				continue
			}
			if *jsonOutput {
				return writeJSON(stdout, agent)
			}
			if _, err := fmt.Fprintf(stdout, "%s (%s)\n", agent.Name, agent.Mode); err != nil {
				return 1
			}
			if agent.Description != "" {
				if _, err := fmt.Fprintf(stdout, "%s\n", agent.Description); err != nil {
					return 1
				}
			}
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "agent not found: %s\n", name)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "unknown agent command: %s\n", fs.Arg(0))
		return 2
	}
}

func sortAgentsForCLI(agents []integration.AgentInfo) {
	slices.SortFunc(agents, func(a integration.AgentInfo, b integration.AgentInfo) int {
		if a.Native != b.Native {
			if a.Native {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

type mcpListItem struct {
	Name   string                `json:"name"`
	Type   string                `json:"type,omitempty"`
	Status integration.MCPStatus `json:"status"`
	Config integration.MCPConfig `json:"config,omitempty"`
}

func mcpCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover MCP config")
	jsonOutput := fs.Bool("json", false, "write MCP status JSON")
	paramsJSON := fs.String("params", "{}", "tool arguments as JSON object for mcp call")
	paramsFile := fs.String("params-file", "", "path to JSON file containing tool arguments, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode mcp [--directory DIR] [--json] COMMAND")
		return 2
	}
	switch fs.Arg(0) {
	case "list", "ls":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode mcp [--directory DIR] [--json] list")
			return 2
		}
		return mcpList(ctx, *directory, *jsonOutput, stdout, stderr)
	case "tools":
		if fs.NArg() != 2 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode mcp [--directory DIR] [--json] tools SERVER")
			return 2
		}
		return mcpTools(ctx, *directory, fs.Arg(1), *jsonOutput, stdout, stderr)
	case "call":
		if fs.NArg() != 3 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode mcp [--directory DIR] [--params JSON | --params-file PATH] call SERVER TOOL")
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
		return mcpCall(ctx, *directory, fs.Arg(1), fs.Arg(2), params, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown mcp command: %s\n", fs.Arg(0))
		return 2
	}
}

func mcpList(ctx context.Context, directory string, jsonOutput bool, stdout io.Writer, stderr io.Writer) int {
	servers, err := integration.LoadConfiguredMCPServers(directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load mcp config failed: %v\n", err)
		return 1
	}
	if len(servers) == 0 {
		if jsonOutput {
			return writeJSON(stdout, []mcpListItem{})
		}
		_, _ = fmt.Fprintln(stdout, "No MCP servers configured")
		return 0
	}
	manager := integration.NewMCPManager()
	defer disconnectMCPServers(manager, servers)
	items := make([]mcpListItem, 0, len(servers))
	for _, server := range servers {
		status := integration.MCPStatus{Status: "disabled"}
		if !server.Disabled {
			statuses := manager.Add(ctx, server.Name, server.Config)
			status = statuses[server.Name]
		}
		items = append(items, mcpListItem{
			Name:   server.Name,
			Type:   server.Config.Type,
			Status: status,
			Config: server.Config,
		})
	}
	if jsonOutput {
		return writeJSON(stdout, items)
	}
	for _, item := range items {
		icon := mcpStatusIcon(item.Status.Status)
		if _, err := fmt.Fprintf(stdout, "%s %s %s\n", icon, item.Name, item.Status.Status); err != nil {
			return 1
		}
		hint := mcpConfigHint(item.Config)
		if hint != "" {
			if _, err := fmt.Fprintf(stdout, "  %s\n", hint); err != nil {
				return 1
			}
		}
		if item.Status.Error != "" {
			if _, err := fmt.Fprintf(stdout, "  %s\n", item.Status.Error); err != nil {
				return 1
			}
		}
	}
	return 0
}

func mcpTools(ctx context.Context, directory string, serverName string, jsonOutput bool, stdout io.Writer, stderr io.Writer) int {
	manager, servers, closeManager, err := openConfiguredMCPManager(ctx, directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load mcp config failed: %v\n", err)
		return 1
	}
	defer closeManager()
	if !configuredMCPServerExists(servers, serverName) {
		_, _ = fmt.Fprintf(stderr, "mcp server not found: %s\n", serverName)
		return 2
	}
	tools, err := manager.Tools(ctx, serverName)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp tools failed: %v\n", err)
		return 1
	}
	if jsonOutput {
		return writeJSON(stdout, tools)
	}
	for _, tool := range tools {
		if _, err := fmt.Fprintf(stdout, "%s\n", tool.Name); err != nil {
			return 1
		}
		if tool.Description != "" {
			if _, err := fmt.Fprintf(stdout, "  %s\n", tool.Description); err != nil {
				return 1
			}
		}
	}
	return 0
}

func mcpCall(ctx context.Context, directory string, serverName string, toolName string, params map[string]any, stdout io.Writer, stderr io.Writer) int {
	manager, servers, closeManager, err := openConfiguredMCPManager(ctx, directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load mcp config failed: %v\n", err)
		return 1
	}
	defer closeManager()
	if !configuredMCPServerExists(servers, serverName) {
		_, _ = fmt.Fprintf(stderr, "mcp server not found: %s\n", serverName)
		return 2
	}
	result, err := manager.CallTool(ctx, serverName, toolName, params)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp call failed: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

func configuredMCPServerExists(servers []integration.MCPConfiguredServer, name string) bool {
	for _, server := range servers {
		if server.Name == name && !server.Disabled {
			return true
		}
	}
	return false
}

func disconnectMCPServers(manager *integration.MCPManager, servers []integration.MCPConfiguredServer) {
	for _, server := range servers {
		manager.Disconnect(server.Name)
	}
}

func mcpStatusIcon(status string) string {
	switch status {
	case "connected":
		return "ok"
	case "disabled":
		return "--"
	default:
		return "!!"
	}
}

func mcpConfigHint(config integration.MCPConfig) string {
	switch config.Type {
	case "remote":
		return config.URL
	case "local":
		return strings.Join(config.Command, " ")
	default:
		return ""
	}
}

func ptyCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("pty", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "write PTY data JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode pty [--json] COMMAND")
		return 2
	}
	switch fs.Arg(0) {
	case "shells":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode pty [--json] shells")
			return 2
		}
		shells := integration.Shells()
		if *jsonOutput {
			return writeJSON(stdout, shells)
		}
		for _, shell := range shells {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\t%v\n", shell["name"], shell["path"], shell["acceptable"]); err != nil {
				return 1
			}
		}
		return 0
	case "run":
		return ptyRun(ctx, fs.Args()[1:], *jsonOutput, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown pty command: %s\n", fs.Arg(0))
		return 2
	}
}

func ptyRun(ctx context.Context, args []string, jsonOutput bool, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("pty run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	command := fs.String("command", "", "command to execute")
	cwd := fs.String("cwd", "", "working directory")
	title := fs.String("title", "", "session title")
	timeout := fs.Duration("timeout", 5*time.Second, "maximum time to wait for command output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *command == "" {
		_, _ = fmt.Fprintln(stderr, "usage: opencode pty [--json] run --command COMMAND [--cwd DIR] [--timeout DURATION]")
		return 2
	}
	manager := integration.NewPTYManager()
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	info, err := manager.Create(runCtx, integration.PTYCreateInput{
		Command: "/bin/sh",
		Args:    []string{"-c", *command},
		CWD:     *cwd,
		Title:   *title,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pty run failed: %v\n", err)
		return 1
	}
	defer manager.Remove(info.ID)
	info = waitForPTYExit(runCtx, manager, info.ID)
	output, _ := manager.Buffer(info.ID)
	if jsonOutput {
		return writeJSON(stdout, map[string]any{
			"info":   info,
			"output": output,
		})
	}
	if _, err := fmt.Fprint(stdout, output); err != nil {
		return 1
	}
	if runCtx.Err() != nil && info.Status != "exited" {
		_, _ = fmt.Fprintf(stderr, "pty run timed out after %s\n", timeout.String())
		return 1
	}
	return 0
}

func waitForPTYExit(ctx context.Context, manager *integration.PTYManager, id string) integration.PTYInfo {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, ok := manager.Get(id)
		if !ok || info.Status == "exited" {
			return info
		}
		select {
		case <-ctx.Done():
			return info
		case <-ticker.C:
		}
	}
}

func skillsCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to discover skills")
	jsonOutput := fs.Bool("json", false, "write skill data JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode skills [--directory DIR] [--json] COMMAND")
		return 2
	}
	skills, err := integration.ListSkills(*directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list skills failed: %v\n", err)
		return 1
	}
	switch fs.Arg(0) {
	case "list":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode skills [--directory DIR] [--json] list")
			return 2
		}
		if *jsonOutput {
			return writeJSON(stdout, skills)
		}
		for _, skill := range skills {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\n", skill.Name, skill.Location); err != nil {
				return 1
			}
		}
		return 0
	case "get":
		if fs.NArg() != 2 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode skills [--directory DIR] [--json] get NAME")
			return 2
		}
		name := fs.Arg(1)
		for _, skill := range skills {
			if skill.Name != name {
				continue
			}
			if *jsonOutput {
				return writeJSON(stdout, skill)
			}
			if _, err := fmt.Fprintln(stdout, strings.TrimSpace(skill.Content)); err != nil {
				return 1
			}
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "skill not found: %s\n", name)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "unknown skills command: %s\n", fs.Arg(0))
		return 2
	}
}

func formattersCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("formatters", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "directory used to load formatter config")
	jsonOutput := fs.Bool("json", false, "write formatter data JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode formatters [--directory DIR] [--json] COMMAND")
		return 2
	}
	switch fs.Arg(0) {
	case "list":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode formatters [--directory DIR] [--json] list")
			return 2
		}
		formatters, err := integration.FormatterStatuses(*directory)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "list formatters failed: %v\n", err)
			return 1
		}
		if *jsonOutput {
			return writeJSON(stdout, formatters)
		}
		for _, formatter := range formatters {
			if _, err := fmt.Fprintf(stdout, "%s\t%v\t%s\n", formatter.Name, formatter.Enabled, strings.Join(formatter.Extensions, ",")); err != nil {
				return 1
			}
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown formatters command: %s\n", fs.Arg(0))
		return 2
	}
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

func dbCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("db", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; relative paths resolve under the opencode data directory")
	format := fs.String("format", "tsv", "query output format: tsv or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := resolvePersistentDBPath(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "resolve db path failed: %v\n", err)
		return 1
	}
	if fs.NArg() == 0 {
		return dbShell(ctx, path, stderr)
	}
	switch fs.Arg(0) {
	case "path":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode db [--db PATH] path")
			return 2
		}
		if _, err := fmt.Fprintln(stdout, path); err != nil {
			return 1
		}
		return 0
	case "migrate":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode db [--db PATH] migrate")
			return 2
		}
		store, err := storage.OpenSQLiteSessionStore(path)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Migration failed: %v\n", err)
			return 1
		}
		if err := store.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "close db failed: %v\n", err)
			return 1
		}
		if _, err := fmt.Fprintln(stdout, "Migration complete: schema ready"); err != nil {
			return 1
		}
		return 0
	case "query":
		if fs.NArg() != 2 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode db [--db PATH] [--format tsv|json] query SQL")
			return 2
		}
		return dbQuery(ctx, path, fs.Arg(1), *format, stdout, stderr)
	case "shell":
		if fs.NArg() != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: opencode db [--db PATH] shell")
			return 2
		}
		return dbShell(ctx, path, stderr)
	default:
		if fs.NArg() == 1 {
			return dbQuery(ctx, path, fs.Arg(0), *format, stdout, stderr)
		}
		_, _ = fmt.Fprintf(stderr, "unknown db command: %s\n", fs.Arg(0))
		return 2
	}
}

func dbQuery(ctx context.Context, path string, query string, format string, stdout io.Writer, stderr io.Writer) int {
	if format != "tsv" && format != "json" {
		_, _ = fmt.Fprintf(stderr, "unsupported db format: %s\n", format)
		return 2
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "close db failed: %v\n", err)
		}
	}()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer func() {
		if err := rows.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "close query rows failed: %v\n", err)
		}
	}()
	result, err := scanDBRows(rows)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read query rows failed: %v\n", err)
		return 1
	}
	if format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return 1
		}
		return 0
	}
	if len(result.Rows) == 0 {
		return 0
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(result.Columns, "\t")); err != nil {
		return 1
	}
	for _, row := range result.Rows {
		values := make([]string, 0, len(result.Columns))
		for _, column := range result.Columns {
			values = append(values, dbCellString(row[column]))
		}
		if _, err := fmt.Fprintln(stdout, strings.Join(values, "\t")); err != nil {
			return 1
		}
	}
	return 0
}

func sqliteReadOnlyDSN(path string) string {
	if path == ":memory:" {
		return path
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
}

type dbQueryResult struct {
	Columns []string
	Rows    []map[string]any
}

func (result dbQueryResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(result.Rows)
}

func scanDBRows(rows *sql.Rows) (dbQueryResult, error) {
	columns, err := rows.Columns()
	if err != nil {
		return dbQueryResult{}, err
	}
	result := dbQueryResult{Columns: columns, Rows: []map[string]any{}}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return dbQueryResult{}, err
		}
		row := map[string]any{}
		for i, column := range columns {
			row[column] = normalizeDBValue(values[i])
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return dbQueryResult{}, err
	}
	return result, nil
}

func normalizeDBValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
}

func dbCellString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func dbShell(ctx context.Context, path string, stderr io.Writer) int {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		_, _ = fmt.Fprintln(stderr, "sqlite3 executable not found; install sqlite3 or use `opencode db query SQL`")
		return 1
	}
	cmd := exec.CommandContext(ctx, "sqlite3", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(stderr, "sqlite3 failed: %v\n", err)
		return 1
	}
	return 0
}

func exportCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; empty uses OPENCODE_DB or in-memory storage")
	sanitize := fs.Bool("sanitize", false, "redact sensitive transcript and file data")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode export [--db PATH] [--sanitize] [SESSION_ID]")
		return 2
	}
	sessionRepo, messageRepo, _, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer closeRepo()

	var info session.Info
	if fs.NArg() == 1 {
		id, err := session.ParseID(fs.Arg(0))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "invalid session id: %v\n", err)
			return 2
		}
		info, err = sessionRepo.Get(ctx, id)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Session not found: %s\n", id)
			return 1
		}
	} else {
		list, err := sessionRepo.List(ctx, session.ListFilter{Limit: 1})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "list sessions failed: %v\n", err)
			return 1
		}
		if len(list) == 0 {
			_, _ = fmt.Fprintln(stderr, "No sessions found")
			return 1
		}
		info = list[0]
	}
	messages, err := messageRepo.Messages(ctx, info.ID, 0)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "export messages failed: %v\n", err)
		return 1
	}
	result := exportData{Info: info, Messages: messages}
	if *sanitize {
		result = sanitizeExport(result)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return 1
	}
	return 0
}

func importCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite database path; empty uses OPENCODE_DB or in-memory storage")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode import [--db PATH] FILE")
		return 2
	}
	source := fs.Arg(0)
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		_, _ = fmt.Fprintln(stderr, "import from share URL is not migrated to Go yet")
		return 2
	}
	data, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(stdout, "File not found: %s\n", source)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "read import file failed: %v\n", err)
		return 1
	}
	var imported exportData
	if err := json.Unmarshal(data, &imported); err != nil {
		_, _ = fmt.Fprintf(stderr, "decode import JSON failed: %v\n", err)
		return 1
	}
	sessionRepo, messageRepo, _, closeRepo, err := openRepositories(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open db failed: %v\n", err)
		return 1
	}
	defer closeRepo()
	importer, ok := sessionRepo.(session.ImportRepository)
	if !ok {
		if candidate, ok := messageRepo.(session.ImportRepository); ok {
			importer = candidate
		}
	}
	if importer == nil {
		_, _ = fmt.Fprintln(stderr, "session store does not support import")
		return 1
	}
	if err := importer.ImportSession(ctx, imported.Info, imported.Messages); err != nil {
		_, _ = fmt.Fprintf(stderr, "import session failed: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Imported session: %s\n", imported.Info.ID); err != nil {
		return 1
	}
	return 0
}

type exportData struct {
	Info     session.Info        `json:"info"`
	Messages []session.WithParts `json:"messages"`
}

func sanitizeExport(input exportData) exportData {
	output := input
	output.Info.Title = redactExportString("session-title", string(output.Info.ID), output.Info.Title)
	output.Info.Directory = redactExportString("session-directory", string(output.Info.ID), output.Info.Directory)
	if output.Info.Revert != nil {
		revert := *output.Info.Revert
		revert.Snapshot = redactExportString("revert-snapshot", string(output.Info.ID), revert.Snapshot)
		revert.Diff = redactExportString("revert-diff", string(output.Info.ID), revert.Diff)
		output.Info.Revert = &revert
	}
	output.Messages = make([]session.WithParts, len(input.Messages))
	for i, message := range input.Messages {
		output.Messages[i] = sanitizeExportMessage(message)
	}
	return output
}

func sanitizeExportMessage(message session.WithParts) session.WithParts {
	message.Info.System = redactExportString("system", string(message.Info.ID), message.Info.System)
	if message.Info.Path != nil {
		path := *message.Info.Path
		path.CWD = redactExportString("cwd", string(message.Info.ID), path.CWD)
		path.Root = redactExportString("root", string(message.Info.ID), path.Root)
		message.Info.Path = &path
	}
	parts := message.Parts
	message.Parts = make([]session.Part, len(parts))
	for i, part := range parts {
		message.Parts[i] = sanitizeExportPart(part)
	}
	return message
}

func sanitizeExportPart(part session.Part) session.Part {
	part.Data = cloneExportMap(part.Data)
	id := string(part.ID)
	switch part.Type {
	case "text":
		redactExportMapString(part.Data, "text", "text", id)
		redactExportMapObject(part.Data, "metadata", "text-metadata", id)
	case "reasoning":
		redactExportMapString(part.Data, "text", "reasoning", id)
		redactExportMapObject(part.Data, "metadata", "reasoning-metadata", id)
	case "file":
		redactExportMapString(part.Data, "url", "file-url", id)
		redactExportMapString(part.Data, "filename", "file-name", id)
		if source, ok := part.Data["source"].(map[string]any); ok {
			redactExportMapString(source, "path", "file-path", id)
			redactExportMapString(source, "name", "file-symbol", id)
			redactExportMapString(source, "uri", "file-uri", id)
			redactExportMapString(source, "clientName", "file-client", id)
			redactExportSpan(source, "text", id)
		}
	case "tool":
		redactExportMapObject(part.Data, "metadata", "tool-metadata", id)
		if state, ok := part.Data["state"].(map[string]any); ok {
			redactExportMapObject(state, "input", "tool-input", id)
			redactExportMapString(state, "raw", "tool-raw", id)
			redactExportMapString(state, "title", "tool-title", id)
			redactExportMapString(state, "output", "tool-output", id)
			redactExportMapObject(state, "metadata", "tool-state-metadata", id)
		}
	case "patch":
		redactExportMapString(part.Data, "hash", "patch", id)
		if files, ok := part.Data["files"].([]any); ok {
			next := make([]any, len(files))
			for i, file := range files {
				if value, ok := file.(string); ok {
					next[i] = redactExportString("patch-file", fmt.Sprintf("%s-%d", id, i), value)
				} else {
					next[i] = file
				}
			}
			part.Data["files"] = next
		}
	case "snapshot":
		redactExportMapString(part.Data, "snapshot", "snapshot", id)
	case "step-start", "step-finish":
		redactExportMapString(part.Data, "snapshot", "snapshot", id)
	case "subtask":
		redactExportMapString(part.Data, "prompt", "subtask-prompt", id)
		redactExportMapString(part.Data, "description", "subtask-description", id)
		redactExportMapString(part.Data, "command", "subtask-command", id)
	}
	return part
}

func redactExportMapString(values map[string]any, key string, kind string, id string) {
	if value, ok := values[key].(string); ok {
		values[key] = redactExportString(kind, id, value)
	}
}

func redactExportMapObject(values map[string]any, key string, kind string, id string) {
	record, ok := values[key].(map[string]any)
	if !ok || len(record) == 0 {
		return
	}
	values[key] = map[string]any{"redacted": fmt.Sprintf("%s:%s", kind, id)}
}

func redactExportSpan(values map[string]any, key string, id string) {
	span, ok := values[key].(map[string]any)
	if !ok {
		return
	}
	if value, ok := span["value"].(string); ok {
		span["value"] = redactExportString("file-text", id, value)
	}
}

func redactExportString(kind string, id string, value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	return fmt.Sprintf("[redacted:%s:%s]", kind, id)
}

func cloneExportMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneExportValue(value)
	}
	return output
}

func cloneExportValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneExportMap(typed)
	case []any:
		output := make([]any, len(typed))
		for i, item := range typed {
			output[i] = cloneExportValue(item)
		}
		return output
	default:
		return typed
	}
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
  stats [--db PATH] [--json] [--days N] [--tools N] [--models[=N]]
  config [--directory DIR] [--worktree DIR]
  commands [--directory DIR]
  db [--db PATH] [--format tsv|json] COMMAND [QUERY]
  debug file COMMAND
  export [--db PATH] [--sanitize] [SESSION_ID]
  import [--db PATH] FILE
  agent [--directory DIR] [--json] COMMAND
  mcp [--directory DIR] [--json] COMMAND
  pty [--json] COMMAND
  skills [--directory DIR] [--json] COMMAND
  formatters [--directory DIR] [--json] COMMAND
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
