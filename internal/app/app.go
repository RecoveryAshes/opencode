// Package app owns the Go command-line surface while migration moves behavior
// out of the legacy TypeScript package.
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/domain/session/retry"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
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
	case "retry-delay":
		return retryDelay(args[1:], stdout, stderr)
	case "providers":
		return providers(args[1:], stdout, stderr)
	case "tools":
		return tools(args[1:], stdout, stderr)
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

	sessionRepo, closeRepo, err := openSessionRepository(*dbPath)
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

func openSessionRepository(dbPath string) (server.SessionRepository, func(), error) {
	if dbPath == "" {
		return storage.NewMemorySessionStore(), func() {}, nil
	}
	store, err := storage.OpenSQLiteSessionStore(dbPath)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() {
		_ = store.Close()
	}, nil
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

func printUsage(w io.Writer) error {
	_, err := fmt.Fprintln(w, `opencode

commands:
  version
  serve [--hostname HOST] [--port PORT] [--db PATH]
  providers
  tools
  retry-delay --attempt N [--retry-after-ms MS | --retry-after VALUE]`)
	return err
}
