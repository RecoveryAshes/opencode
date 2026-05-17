# OpenCode Go Migration

This branch is the dedicated Go migration branch for `RecoveryAshes/opencode`.

## Scope

Migrated into Go:

- CLI entrypoints: `cmd/opencode`, `cmd/opencode-sidecar`
- Headless sidecar foundation: `internal/server`
- Session domain contracts: `internal/domain/session`
- Session retry behavior: `internal/domain/session/retry`
- Session overflow behavior: `internal/domain/session/overflow`
- First storage seam: `internal/storage`
- Provider inventory and contracts: `internal/llm`
- Local tool integration inventory: `internal/integration`

Excluded from the Go target:

- console, zen, enterprise
- Cloudflare Workers and SST infrastructure
- Slack and cloud deployment paths
- SDK publication

## Gate Decision

- Gate: PRE_CODE_DDD
- Decision: PASS
- Risk level: HIGH
- Scope reviewed: `packages/opencode/src/index.ts`, CLI commands, HTTP API groups/handlers, server bootstrap, session retry/overflow sources and tests, provider package inventory, tool inventory.
- Evidence inspected: `packages/opencode/src/session/retry.ts`, `packages/opencode/src/session/overflow.ts`, `packages/opencode/test/session/retry.test.ts`, `packages/opencode/test/session/compaction.test.ts`, `packages/opencode/src/server/routes/instance/httpapi/groups/session.ts`, `packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts`.
- Blocking issues: a single commit cannot safely replace the full TypeScript runtime because OpenCode spans CLI, TUI, HTTP/SSE/WebSocket, SQLite migrations, Effect services, provider SDKs, MCP, LSP, PTY, plugins, and Electron.
- Allowed next action: continue migrating one parity-tested vertical slice at a time on this `go` branch.

## Current Parity Slices

### Session Retry

Source:

- `packages/opencode/src/session/retry.ts`
- `packages/opencode/test/session/retry.test.ts`

Go target:

- `internal/domain/session/retry`

Covered behavior:

- exponential retry backoff
- `retry-after-ms` and `retry-after` precedence
- JavaScript `parseFloat` prefix behavior
- HTTP date retry parsing
- 5xx retry even when SDK metadata is non-retryable
- context overflow exclusion
- free-tier and Go usage-limit action metadata

### Session Overflow

Source:

- `packages/opencode/src/session/overflow.ts`
- `packages/opencode/test/session/compaction.test.ts`

Go target:

- `internal/domain/session/overflow`

Covered behavior:

- usable context calculation
- input/output limit interaction
- configured compaction reserve
- cache read/write token accounting
- total token override
- disabled auto-compaction

## Verification

Use:

```sh
./script/go-verify
```

This runs:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `golangci-lint run`
- `go build ./cmd/opencode ./cmd/opencode-sidecar`

## Next Migration Slices

1. Freeze TypeScript HTTP/OpenAPI/SSE/WebSocket snapshots and compare them against `internal/server`.
2. Port SQLite schema and migrations behind storage repositories.
3. Port session create/list/get/message flows and event bus semantics.
4. Port file, shell, grep/glob, LSP, PTY, MCP, and plugin tools.
5. Port providers one adapter at a time with golden normalized error and stream tests.
6. Rebuild the TUI in Go and reduce Electron main to sidecar lifecycle plus local client calls.
