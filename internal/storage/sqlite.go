package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	syncdomain "github.com/RecoveryAshes/opencode/internal/domain/sync"
	_ "modernc.org/sqlite"
)

const defaultProjectID = "go-local"

// SQLiteSessionStore persists migrated session data into the legacy opencode
// SQLite table names and core columns.
type SQLiteSessionStore struct {
	db *sql.DB
}

// OpenSQLiteSessionStore opens a SQLite database and applies the migrated core schema.
func OpenSQLiteSessionStore(path string) (*SQLiteSessionStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &SQLiteSessionStore{db: db}
	if err := store.configure(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database connection.
func (store *SQLiteSessionStore) Close() error {
	return store.db.Close()
}

// List returns sessions sorted by update time descending.
func (store *SQLiteSessionStore) List(ctx context.Context, filter session.ListFilter) ([]session.Info, error) {
	query := sessionSelectColumns + `
FROM session`
	args := []any{}
	conditions := []string{}
	if filter.Search != "" {
		conditions = append(conditions, "lower(title) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.Search)+"%")
	}
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if filter.WorkspaceID != "" {
		conditions = append(conditions, "workspace_id = ?")
		args = append(args, filter.WorkspaceID)
	}
	if filter.Roots {
		conditions = append(conditions, "parent_id IS NULL")
	}
	if filter.Start > 0 {
		conditions = append(conditions, "time_updated >= ?")
		args = append(args, filter.Start)
	}
	if filter.Path != nil {
		if *filter.Path == "" {
			conditions = append(conditions, "(path IS NULL OR path = '')")
		} else {
			if filter.Directory != "" {
				conditions = append(conditions, "(path = ? OR path LIKE ? OR ((path IS NULL OR path = '') AND directory = ?))")
				args = append(args, *filter.Path, *filter.Path+"/%", filter.Directory)
			} else {
				conditions = append(conditions, "(path = ? OR path LIKE ?)")
				args = append(args, *filter.Path, *filter.Path+"/%")
			}
		}
	} else if filter.Scope != "project" && filter.Directory != "" {
		conditions = append(conditions, "directory = ?")
		args = append(args, filter.Directory)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY time_updated DESC, id DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := []session.Info{}
	for rows.Next() {
		info, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return result, nil
}

// Create stores a new session.
func (store *SQLiteSessionStore) Create(ctx context.Context, input session.CreateInput) (session.Info, error) {
	id, err := session.NewID()
	if err != nil {
		return session.Info{}, err
	}
	now := session.NowMillis()
	projectID := defaultString(input.ProjectID, defaultProjectID)
	directory := defaultString(input.Directory, ".")
	if err := store.ensureProject(ctx, projectID, directory, now); err != nil {
		return session.Info{}, err
	}

	info := session.Info{
		ID:          id,
		Slug:        slug(input.Title),
		ProjectID:   projectID,
		WorkspaceID: input.WorkspaceID,
		Directory:   directory,
		Path:        input.Path,
		ParentID:    input.ParentID,
		Title:       input.Title,
		Agent:       input.Agent,
		Model:       cloneModel(input.Model),
		Version:     defaultString(input.Version, "go-migration"),
		Cost:        input.Cost,
		Tokens:      cloneTokens(input.Tokens),
		Time: session.TimeInfo{
			Created: now,
			Updated: now,
		},
	}
	parentID := sql.NullString{}
	if input.ParentID != nil {
		parentID = sql.NullString{String: string(*input.ParentID), Valid: true}
	}
	modelJSON, err := optionalJSON(info.Model)
	if err != nil {
		return session.Info{}, fmt.Errorf("encode model: %w", err)
	}
	tokens := emptyTokens(info.Tokens)
	_, err = store.db.ExecContext(ctx, `INSERT INTO session (
id, project_id, workspace_id, parent_id, slug, directory, path, title, version, permission, agent, model, cost,
tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write, time_created, time_updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		info.ProjectID,
		nullableString(info.WorkspaceID, info.WorkspaceID != ""),
		parentID,
		info.Slug,
		info.Directory,
		nullableString(info.Path, info.Path != ""),
		input.Title,
		info.Version,
		"[]",
		nullableString(info.Agent, info.Agent != ""),
		nullableString(modelJSON, info.Model != nil),
		info.Cost,
		tokens.Input,
		tokens.Output,
		tokens.Reasoning,
		tokens.Cache.Read,
		tokens.Cache.Write,
		now,
		now,
	)
	if err != nil {
		return session.Info{}, fmt.Errorf("create session: %w", err)
	}
	return info, nil
}

// Get returns one session.
func (store *SQLiteSessionStore) Get(ctx context.Context, id session.ID) (session.Info, error) {
	row := store.db.QueryRowContext(ctx, sessionSelectColumns+`
FROM session WHERE id = ?`, id)
	return scanSession(row)
}

// Update changes mutable session fields.
func (store *SQLiteSessionStore) Update(ctx context.Context, id session.ID, input session.UpdateInput) (session.Info, error) {
	current, err := store.Get(ctx, id)
	if err != nil {
		return session.Info{}, err
	}
	if input.Title != nil {
		current.Title = *input.Title
		current.Slug = slug(*input.Title)
	}
	if input.ProjectID != nil {
		current.ProjectID = *input.ProjectID
	}
	if input.WorkspaceID != nil {
		current.WorkspaceID = *input.WorkspaceID
	}
	if input.Directory != nil {
		current.Directory = *input.Directory
	}
	if input.Path != nil {
		current.Path = *input.Path
	}
	if input.Agent != nil {
		current.Agent = *input.Agent
	}
	if input.Model != nil {
		current.Model = cloneModel(input.Model)
	}
	if input.Version != nil {
		current.Version = *input.Version
	}
	if input.Cost != nil {
		current.Cost = *input.Cost
	}
	if input.Tokens != nil {
		current.Tokens = cloneTokens(input.Tokens)
	}
	if input.Archived != nil {
		current.Time.Archived = input.Archived
	}
	if input.Compacting != nil {
		current.Time.Compacting = input.Compacting
	}
	if input.ClearCompact {
		current.Time.Compacting = nil
	}
	if input.Permission != nil {
		current.Permission = append([]string(nil), (*input.Permission)...)
	}
	if input.Revert != nil {
		revert := *input.Revert
		current.Revert = &revert
	}
	if input.ClearRevert {
		current.Revert = nil
		current.Summary = nil
	}
	if input.Summary != nil {
		summary := *input.Summary
		current.Summary = &summary
	}
	if input.Share != nil {
		share := *input.Share
		current.Share = &share
	}
	if input.ClearShare {
		current.Share = nil
	}
	permissionJSON, err := json.Marshal(current.Permission)
	if err != nil {
		return session.Info{}, fmt.Errorf("encode permission: %w", err)
	}
	summaryDiffs, err := json.Marshal(summaryDiffs(current.Summary))
	if err != nil {
		return session.Info{}, fmt.Errorf("encode summary diffs: %w", err)
	}
	revertJSON, err := optionalJSON(current.Revert)
	if err != nil {
		return session.Info{}, fmt.Errorf("encode revert: %w", err)
	}
	modelJSON, err := optionalJSON(current.Model)
	if err != nil {
		return session.Info{}, fmt.Errorf("encode model: %w", err)
	}
	tokens := emptyTokens(current.Tokens)
	shareURL := sql.NullString{}
	if current.Share != nil && current.Share.URL != "" {
		shareURL = sql.NullString{String: current.Share.URL, Valid: true}
	}
	archived := sql.NullInt64{}
	if current.Time.Archived != nil {
		archived = sql.NullInt64{Int64: *current.Time.Archived, Valid: true}
	}
	compacting := sql.NullInt64{}
	if current.Time.Compacting != nil {
		compacting = sql.NullInt64{Int64: *current.Time.Compacting, Valid: true}
	}
	if err := store.ensureProject(ctx, current.ProjectID, current.Directory, session.NowMillis()); err != nil {
		return session.Info{}, err
	}
	summary := current.Summary
	result, err := store.db.ExecContext(ctx, `UPDATE session SET
title = ?, slug = ?, project_id = ?, workspace_id = ?, directory = ?, path = ?, agent = ?, model = ?, version = ?,
cost = ?, tokens_input = ?, tokens_output = ?, tokens_reasoning = ?, tokens_cache_read = ?, tokens_cache_write = ?,
permission = ?, time_updated = ?, time_archived = ?, share_url = ?,
summary_additions = ?, summary_deletions = ?, summary_files = ?, summary_diffs = ?, revert = ?, time_compacting = ?
WHERE id = ?`,
		current.Title,
		current.Slug,
		current.ProjectID,
		nullableString(current.WorkspaceID, current.WorkspaceID != ""),
		current.Directory,
		nullableString(current.Path, current.Path != ""),
		nullableString(current.Agent, current.Agent != ""),
		nullableString(modelJSON, current.Model != nil),
		current.Version,
		current.Cost,
		tokens.Input,
		tokens.Output,
		tokens.Reasoning,
		tokens.Cache.Read,
		tokens.Cache.Write,
		string(permissionJSON),
		session.NowMillis(),
		archived,
		shareURL,
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Additions }),
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Deletions }),
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Files }),
		nullableString(string(summaryDiffs), summary != nil && len(summary.Diffs) > 0),
		nullableString(revertJSON, current.Revert != nil),
		compacting,
		id,
	)
	if err != nil {
		return session.Info{}, fmt.Errorf("update session: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return session.Info{}, fmt.Errorf("update session rows affected: %w", err)
	}
	if count == 0 {
		return session.Info{}, session.ErrNotFound
	}
	return store.Get(ctx, id)
}

// Children lists child sessions by parent id.
func (store *SQLiteSessionStore) Children(ctx context.Context, parentID session.ID) ([]session.Info, error) {
	if _, err := store.Get(ctx, parentID); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, sessionSelectColumns+`
FROM session WHERE parent_id = ? ORDER BY time_updated DESC, id DESC`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []session.Info{}
	for rows.Next() {
		info, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate children: %w", err)
	}
	return result, nil
}

// Fork creates a child session and copies messages up to the optional message id.
func (store *SQLiteSessionStore) Fork(ctx context.Context, parentID session.ID, messageID *session.MessageID) (session.Info, error) {
	parent, err := store.Get(ctx, parentID)
	if err != nil {
		return session.Info{}, err
	}
	child, err := store.Create(ctx, session.CreateInput{
		Title:       parent.Title,
		ParentID:    &parentID,
		ProjectID:   parent.ProjectID,
		WorkspaceID: parent.WorkspaceID,
		Directory:   parent.Directory,
		Path:        parent.Path,
		Agent:       parent.Agent,
		Model:       cloneModel(parent.Model),
		Version:     parent.Version,
		Cost:        parent.Cost,
		Tokens:      cloneTokens(parent.Tokens),
	})
	if err != nil {
		return session.Info{}, err
	}
	messages, err := store.Messages(ctx, parentID, 0)
	if err != nil {
		return session.Info{}, err
	}
	for _, message := range copyMessagesForFork(child.ID, messages, messageID) {
		if err := store.insertMessageWithParts(ctx, message); err != nil {
			return session.Info{}, err
		}
	}
	return child, nil
}

// Remove deletes one session.
func (store *SQLiteSessionStore) Remove(ctx context.Context, id session.ID) error {
	result, err := store.db.ExecContext(ctx, `DELETE FROM session WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove session: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove session rows affected: %w", err)
	}
	if count == 0 {
		return session.ErrNotFound
	}
	return nil
}

// ImportSession stores exported session data while preserving IDs.
func (store *SQLiteSessionStore) ImportSession(ctx context.Context, info session.Info, messages []session.WithParts) error {
	if info.ID == "" {
		return errors.New("session id is required")
	}
	if info.Slug == "" {
		info.Slug = slug(info.Title)
	}
	if info.ProjectID == "" {
		info.ProjectID = defaultProjectID
	}
	if info.Directory == "" {
		info.Directory = "."
	}
	if info.Version == "" {
		info.Version = "go-migration"
	}
	if info.Time.Created == 0 {
		info.Time.Created = session.NowMillis()
	}
	if info.Time.Updated == 0 {
		info.Time.Updated = info.Time.Created
	}
	if err := store.ensureProject(ctx, info.ProjectID, info.Directory, info.Time.Created); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := upsertImportedSession(ctx, tx, info); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message WHERE session_id = ?`, info.ID); err != nil {
		return fmt.Errorf("clear imported messages: %w", err)
	}
	for _, message := range normalizeImportedMessages(info.ID, messages) {
		if err := insertMessage(ctx, tx, message); err != nil {
			return err
		}
		for _, part := range message.Parts {
			if err := insertPart(ctx, tx, part); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}
	return nil
}

func (store *SQLiteSessionStore) configure(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA cache_size = -64000`,
		`PRAGMA foreign_keys = ON`,
		projectSchema,
		sessionSchema,
		messageSchema,
		partSchema,
		todoSchema,
		sessionDiffSchema,
		sessionStatusSchema,
		eventSequenceSchema,
		eventSchema,
		`CREATE INDEX IF NOT EXISTS session_project_idx ON session(project_id)`,
		`CREATE INDEX IF NOT EXISTS session_workspace_idx ON session(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS session_parent_idx ON session(parent_id)`,
		`CREATE INDEX IF NOT EXISTS message_session_time_created_id_idx ON message(session_id, time_created, id)`,
		`CREATE INDEX IF NOT EXISTS part_message_id_id_idx ON part(message_id, id)`,
		`CREATE INDEX IF NOT EXISTS part_session_idx ON part(session_id)`,
		`CREATE INDEX IF NOT EXISTS todo_session_idx ON todo(session_id)`,
		`CREATE INDEX IF NOT EXISTS event_aggregate_seq_idx ON event(aggregate_id, seq)`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (store *SQLiteSessionStore) ensureProject(ctx context.Context, projectID string, directory string, now int64) error {
	worktree := filepath.Clean(defaultString(directory, "."))
	_, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO project (
id, worktree, name, time_created, time_updated, sandboxes
) VALUES (?, ?, ?, ?, ?, ?)`, projectID, worktree, filepath.Base(worktree), now, now, "[]")
	if err != nil {
		return fmt.Errorf("ensure project: %w", err)
	}
	return nil
}

// SetTodos replaces the per-session todo list.
func (store *SQLiteSessionStore) SetTodos(ctx context.Context, id session.ID, todos []session.TodoInfo) error {
	if _, err := store.Get(ctx, id); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin todo transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `DELETE FROM todo WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("delete todos: %w", err)
	}
	for position, todo := range todos {
		now := session.NowMillis()
		if _, err := tx.ExecContext(ctx, `INSERT INTO todo (
session_id, content, status, priority, position, time_created, time_updated
) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, todo.Content, todo.Status, todo.Priority, position, now, now); err != nil {
			return fmt.Errorf("insert todo: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit todos: %w", err)
	}
	committed = true
	return nil
}

// Todos returns the per-session todo list in display order.
func (store *SQLiteSessionStore) Todos(ctx context.Context, id session.ID) ([]session.TodoInfo, error) {
	if _, err := store.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT content, status, priority FROM todo WHERE session_id = ? ORDER BY position ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []session.TodoInfo{}
	for rows.Next() {
		var todo session.TodoInfo
		if err := rows.Scan(&todo.Content, &todo.Status, &todo.Priority); err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}
		result = append(result, todo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate todos: %w", err)
	}
	return result, nil
}

// SetStatus stores a runtime status for a session. Idle clears runtime state.
func (store *SQLiteSessionStore) SetStatus(ctx context.Context, id session.ID, status session.StatusInfo) error {
	if _, err := store.Get(ctx, id); err != nil {
		return err
	}
	if status.Type == "" || status.Type == "idle" {
		if _, err := store.db.ExecContext(ctx, `DELETE FROM session_status WHERE session_id = ?`, id); err != nil {
			return fmt.Errorf("delete session status: %w", err)
		}
		return nil
	}
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode session status: %w", err)
	}
	now := session.NowMillis()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO session_status (session_id, data, time_updated)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET data = excluded.data, time_updated = excluded.time_updated`, id, string(data), now); err != nil {
		return fmt.Errorf("set session status: %w", err)
	}
	return nil
}

// Status returns one runtime status, defaulting to idle.
func (store *SQLiteSessionStore) Status(ctx context.Context, id session.ID) (session.StatusInfo, error) {
	if _, err := store.Get(ctx, id); err != nil {
		return session.StatusInfo{}, err
	}
	row := store.db.QueryRowContext(ctx, `SELECT data FROM session_status WHERE session_id = ?`, id)
	status, err := scanStatus(row)
	if errors.Is(err, session.ErrNotFound) {
		return session.StatusInfo{Type: "idle"}, nil
	}
	return status, err
}

// Statuses returns non-idle runtime statuses.
func (store *SQLiteSessionStore) Statuses(ctx context.Context) (map[session.ID]session.StatusInfo, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT session_id, data FROM session_status ORDER BY time_updated DESC, session_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list session statuses: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	result := map[session.ID]session.StatusInfo{}
	for rows.Next() {
		var id string
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan session status: %w", err)
		}
		var status session.StatusInfo
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			return nil, fmt.Errorf("decode session status: %w", err)
		}
		result[session.ID(id)] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session statuses: %w", err)
	}
	return result, nil
}

// SetDiff stores the current session diff snapshot and mirrors summary counts.
func (store *SQLiteSessionStore) SetDiff(ctx context.Context, id session.ID, diffs []map[string]any) error {
	if _, err := store.Get(ctx, id); err != nil {
		return err
	}
	data, err := json.Marshal(diffs)
	if err != nil {
		return fmt.Errorf("encode session diff: %w", err)
	}
	summary := summaryFromDiffs(diffs)
	var additions sql.NullInt64
	var deletions sql.NullInt64
	var files sql.NullInt64
	if summary != nil {
		additions = sql.NullInt64{Int64: int64(summary.Additions), Valid: true}
		deletions = sql.NullInt64{Int64: int64(summary.Deletions), Valid: true}
		files = sql.NullInt64{Int64: int64(summary.Files), Valid: true}
	}
	now := session.NowMillis()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin diff transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if len(diffs) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_diff WHERE session_id = ?`, id); err != nil {
			return fmt.Errorf("delete session diff: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO session_diff (session_id, data, time_updated)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET data = excluded.data, time_updated = excluded.time_updated`, id, string(data), now); err != nil {
		return fmt.Errorf("set session diff: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session SET
summary_additions = ?, summary_deletions = ?, summary_files = ?, summary_diffs = ?, time_updated = ?
WHERE id = ?`, additions, deletions, files, nullableString(string(data), len(diffs) > 0), now, id); err != nil {
		return fmt.Errorf("update session diff summary: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit diff: %w", err)
	}
	committed = true
	return nil
}

// Diff returns the current session diff snapshot.
func (store *SQLiteSessionStore) Diff(ctx context.Context, id session.ID) ([]map[string]any, error) {
	info, err := store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	row := store.db.QueryRowContext(ctx, `SELECT data FROM session_diff WHERE session_id = ?`, id)
	diffs, err := scanDiff(row)
	if errors.Is(err, session.ErrNotFound) {
		if info.Summary != nil && info.Summary.Diffs != nil {
			return cloneDiffs(info.Summary.Diffs), nil
		}
		return []map[string]any{}, nil
	}
	return diffs, err
}

// AppendEvents stores sync events using the legacy event/event_sequence schema.
func (store *SQLiteSessionStore) AppendEvents(aggregateID string, events []syncdomain.Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin sync append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, event := range events {
		rowAggregateID := event.AggregateID
		if rowAggregateID == "" {
			rowAggregateID = aggregateID
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			return fmt.Errorf("marshal sync event: %w", err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO event_sequence (aggregate_id, seq) VALUES (?, ?)`, rowAggregateID, -1); err != nil {
			return fmt.Errorf("ensure event sequence: %w", err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO event (id, aggregate_id, seq, type, data) VALUES (?, ?, ?, ?, ?)`, event.ID, rowAggregateID, event.Seq, event.Type, string(data)); err != nil {
			return fmt.Errorf("insert sync event: %w", err)
		}
		if _, err := tx.Exec(`UPDATE event_sequence SET seq = CASE WHEN seq < ? THEN ? ELSE seq END WHERE aggregate_id = ?`, event.Seq, event.Seq, rowAggregateID); err != nil {
			return fmt.Errorf("update event sequence: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync append: %w", err)
	}
	committed = true
	return nil
}

// EventsAfter returns sync events newer than the supplied aggregate sequence map.
func (store *SQLiteSessionStore) EventsAfter(cursor map[string]int64) ([]syncdomain.HistoryEvent, error) {
	rows, err := store.db.QueryContext(context.Background(), `SELECT id, aggregate_id, seq, type, data FROM event ORDER BY seq ASC, aggregate_id ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list sync events: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := []syncdomain.HistoryEvent{}
	for rows.Next() {
		var item syncdomain.HistoryEvent
		var raw string
		if err := rows.Scan(&item.ID, &item.AggregateID, &item.Seq, &item.Type, &raw); err != nil {
			return nil, fmt.Errorf("scan sync event: %w", err)
		}
		if after, ok := cursor[item.AggregateID]; ok && item.Seq <= after {
			continue
		}
		if err := json.Unmarshal([]byte(raw), &item.Data); err != nil {
			return nil, fmt.Errorf("decode sync event: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sync events: %w", err)
	}
	return result, nil
}

// Messages returns messages in creation order.
func (store *SQLiteSessionStore) Messages(ctx context.Context, sessionID session.ID, limit int) ([]session.WithParts, error) {
	if _, err := store.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ? ORDER BY time_created ASC, rowid ASC`
	args := []any{sessionID}
	if limit > 0 {
		query = `SELECT id, session_id, time_created, time_updated, data FROM (
SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ? ORDER BY time_created DESC, rowid DESC LIMIT ?
) ORDER BY time_created ASC, rowid ASC`
		args = append(args, limit)
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	return store.scanMessages(ctx, rows)
}

// MessagePage returns messages using the legacy before cursor contract.
func (store *SQLiteSessionStore) MessagePage(ctx context.Context, sessionID session.ID, filter session.MessageListFilter) (session.MessagePage, error) {
	if _, err := store.Get(ctx, sessionID); err != nil {
		return session.MessagePage{}, err
	}
	limit := filter.Limit
	if limit <= 0 {
		items, err := store.Messages(ctx, sessionID, 0)
		if err != nil {
			return session.MessagePage{}, err
		}
		return session.MessagePage{Items: items}, nil
	}

	query := `SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ?`
	args := []any{sessionID}
	if filter.Before != nil {
		query += ` AND (time_created < ? OR (time_created = ? AND rowid < (SELECT rowid FROM message WHERE id = ?)))`
		args = append(args, filter.Before.Time, filter.Before.Time, filter.Before.ID)
	}
	query += ` ORDER BY time_created DESC, rowid DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return session.MessagePage{}, fmt.Errorf("page messages: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	items, err := store.scanMessages(ctx, rows)
	if err != nil {
		return session.MessagePage{}, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var cursor *session.MessageCursor
	if more && len(items) > 0 {
		last := items[len(items)-1]
		cursor = &session.MessageCursor{ID: last.Info.ID, Time: last.Info.Time.Created}
	}
	slices.Reverse(items)
	return session.MessagePage{Items: items, More: more, Cursor: cursor}, nil
}

// GetMessage returns a message with its parts.
func (store *SQLiteSessionStore) GetMessage(ctx context.Context, sessionID session.ID, messageID session.MessageID) (session.WithParts, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ? AND id = ?`, sessionID, messageID)
	if err != nil {
		return session.WithParts{}, fmt.Errorf("get message: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	items, err := store.scanMessages(ctx, rows)
	if err != nil {
		return session.WithParts{}, err
	}
	if len(items) == 0 {
		return session.WithParts{}, session.ErrNotFound
	}
	return items[0], nil
}

// CreatePrompt creates a user message and prompt parts.
func (store *SQLiteSessionStore) CreatePrompt(ctx context.Context, sessionID session.ID, input session.PromptInput) (session.WithParts, error) {
	if _, err := store.Get(ctx, sessionID); err != nil {
		return session.WithParts{}, err
	}
	message, err := createPromptMessage(sessionID, input)
	if err != nil {
		return session.WithParts{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return session.WithParts{}, fmt.Errorf("begin prompt transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := insertMessage(ctx, tx, message); err != nil {
		return session.WithParts{}, err
	}
	for _, part := range message.Parts {
		if err := insertPart(ctx, tx, part); err != nil {
			return session.WithParts{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session SET time_updated = ? WHERE id = ?`, session.NowMillis(), sessionID); err != nil {
		return session.WithParts{}, fmt.Errorf("touch session after prompt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return session.WithParts{}, fmt.Errorf("commit prompt: %w", err)
	}
	return message, nil
}

// CreateAssistant creates one assistant message with text and finish parts.
func (store *SQLiteSessionStore) CreateAssistant(ctx context.Context, sessionID session.ID, input session.AssistantInput) (session.WithParts, error) {
	if _, err := store.Get(ctx, sessionID); err != nil {
		return session.WithParts{}, err
	}
	message, err := createAssistantMessage(sessionID, input)
	if err != nil {
		return session.WithParts{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return session.WithParts{}, fmt.Errorf("begin assistant transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := insertMessage(ctx, tx, message); err != nil {
		return session.WithParts{}, err
	}
	for _, part := range message.Parts {
		if err := insertPart(ctx, tx, part); err != nil {
			return session.WithParts{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session SET time_updated = ? WHERE id = ?`, session.NowMillis(), sessionID); err != nil {
		return session.WithParts{}, fmt.Errorf("touch session after assistant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return session.WithParts{}, fmt.Errorf("commit assistant: %w", err)
	}
	return message, nil
}

func (store *SQLiteSessionStore) insertMessageWithParts(ctx context.Context, message session.WithParts) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fork message transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := insertMessage(ctx, tx, message); err != nil {
		return err
	}
	for _, part := range message.Parts {
		if err := insertPart(ctx, tx, part); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fork message: %w", err)
	}
	return nil
}

// RemoveMessage deletes one message and its parts.
func (store *SQLiteSessionStore) RemoveMessage(ctx context.Context, sessionID session.ID, messageID session.MessageID) error {
	result, err := store.db.ExecContext(ctx, `DELETE FROM message WHERE session_id = ? AND id = ?`, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("remove message: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove message rows affected: %w", err)
	}
	if count == 0 {
		return session.ErrNotFound
	}
	return nil
}

// RemovePart deletes one part from a message.
func (store *SQLiteSessionStore) RemovePart(ctx context.Context, sessionID session.ID, messageID session.MessageID, partID session.PartID) error {
	result, err := store.db.ExecContext(ctx, `DELETE FROM part WHERE session_id = ? AND message_id = ? AND id = ?`, sessionID, messageID, partID)
	if err != nil {
		return fmt.Errorf("remove part: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove part rows affected: %w", err)
	}
	if count == 0 {
		return session.ErrNotFound
	}
	return nil
}

// UpdatePart replaces one stored part.
func (store *SQLiteSessionStore) UpdatePart(ctx context.Context, part session.Part) (session.Part, error) {
	data, err := partDataJSON(part)
	if err != nil {
		return session.Part{}, err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE part SET data = ?, time_updated = ? WHERE session_id = ? AND message_id = ? AND id = ?`,
		data,
		session.NowMillis(),
		part.SessionID,
		part.MessageID,
		part.ID,
	)
	if err != nil {
		return session.Part{}, fmt.Errorf("update part: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return session.Part{}, fmt.Errorf("update part rows affected: %w", err)
	}
	if count == 0 {
		return session.Part{}, session.ErrNotFound
	}
	return part, nil
}

type messageRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func (store *SQLiteSessionStore) scanMessages(ctx context.Context, rows messageRows) ([]session.WithParts, error) {
	result := []session.WithParts{}
	for rows.Next() {
		var id string
		var sessionID string
		var created int64
		var updated int64
		var data string
		if err := rows.Scan(&id, &sessionID, &created, &updated, &data); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		info, err := decodeMessageInfo(id, sessionID, created, data)
		if err != nil {
			return nil, err
		}
		parts, err := store.parts(ctx, session.MessageID(id))
		if err != nil {
			return nil, err
		}
		result = append(result, session.WithParts{Info: info, Parts: parts})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return result, nil
}

func (store *SQLiteSessionStore) parts(ctx context.Context, messageID session.MessageID) ([]session.Part, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id, session_id, message_id, data FROM part WHERE message_id = ? ORDER BY time_created ASC, rowid ASC`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list parts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []session.Part{}
	for rows.Next() {
		var id string
		var sessionID string
		var storedMessageID string
		var data string
		if err := rows.Scan(&id, &sessionID, &storedMessageID, &data); err != nil {
			return nil, fmt.Errorf("scan part: %w", err)
		}
		part, err := decodePart(id, sessionID, storedMessageID, data)
		if err != nil {
			return nil, err
		}
		result = append(result, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate parts: %w", err)
	}
	return result, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, message session.WithParts) error {
	data, err := messageInfoJSON(message.Info)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		message.Info.ID,
		message.Info.SessionID,
		message.Info.Time.Created,
		message.Info.Time.Created,
		data,
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

func insertPart(ctx context.Context, tx *sql.Tx, part session.Part) error {
	data, err := partDataJSON(part)
	if err != nil {
		return err
	}
	now := session.NowMillis()
	_, err = tx.ExecContext(ctx, `INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		part.ID,
		part.MessageID,
		part.SessionID,
		now,
		now,
		data,
	)
	if err != nil {
		return fmt.Errorf("insert part: %w", err)
	}
	return nil
}

func upsertImportedSession(ctx context.Context, tx *sql.Tx, info session.Info) error {
	modelJSON, err := optionalJSON(info.Model)
	if err != nil {
		return fmt.Errorf("encode import model: %w", err)
	}
	permissionJSON, err := optionalJSON(info.Permission)
	if err != nil {
		return fmt.Errorf("encode import permission: %w", err)
	}
	revertJSON, err := optionalJSON(info.Revert)
	if err != nil {
		return fmt.Errorf("encode import revert: %w", err)
	}
	summaryDiffsJSON, err := optionalJSON(summaryDiffs(info.Summary))
	if err != nil {
		return fmt.Errorf("encode import summary diffs: %w", err)
	}
	tokens := emptyTokens(info.Tokens)
	var parentID sql.NullString
	if info.ParentID != nil {
		parentID = sql.NullString{String: string(*info.ParentID), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session (
id, project_id, workspace_id, parent_id, slug, directory, path, title, version, share_url,
summary_additions, summary_deletions, summary_files, summary_diffs, cost,
tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
revert, permission, agent, model, time_created, time_updated, time_compacting, time_archived
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
project_id = excluded.project_id,
workspace_id = excluded.workspace_id,
parent_id = excluded.parent_id,
slug = excluded.slug,
directory = excluded.directory,
path = excluded.path,
title = excluded.title,
version = excluded.version,
share_url = excluded.share_url,
summary_additions = excluded.summary_additions,
summary_deletions = excluded.summary_deletions,
summary_files = excluded.summary_files,
summary_diffs = excluded.summary_diffs,
cost = excluded.cost,
tokens_input = excluded.tokens_input,
tokens_output = excluded.tokens_output,
tokens_reasoning = excluded.tokens_reasoning,
tokens_cache_read = excluded.tokens_cache_read,
tokens_cache_write = excluded.tokens_cache_write,
revert = excluded.revert,
permission = excluded.permission,
agent = excluded.agent,
model = excluded.model,
time_created = excluded.time_created,
time_updated = excluded.time_updated,
time_compacting = excluded.time_compacting,
time_archived = excluded.time_archived`,
		info.ID,
		info.ProjectID,
		nullableString(info.WorkspaceID, info.WorkspaceID != ""),
		parentID,
		info.Slug,
		info.Directory,
		nullableString(info.Path, info.Path != ""),
		info.Title,
		info.Version,
		nullableString(shareURLFromInfo(info.Share), info.Share != nil && info.Share.URL != ""),
		nullableInt(info.Summary, func(summary *session.SummaryInfo) int { return summary.Additions }),
		nullableInt(info.Summary, func(summary *session.SummaryInfo) int { return summary.Deletions }),
		nullableInt(info.Summary, func(summary *session.SummaryInfo) int { return summary.Files }),
		nullableString(summaryDiffsJSON, summaryDiffsJSON != ""),
		info.Cost,
		tokens.Input,
		tokens.Output,
		tokens.Reasoning,
		tokens.Cache.Read,
		tokens.Cache.Write,
		nullableString(revertJSON, info.Revert != nil),
		nullableString(permissionJSON, len(info.Permission) > 0),
		nullableString(info.Agent, info.Agent != ""),
		nullableString(modelJSON, info.Model != nil),
		info.Time.Created,
		info.Time.Updated,
		nullableInt(info.Time.Compacting, func(value *int64) int { return int(*value) }),
		nullableInt(info.Time.Archived, func(value *int64) int { return int(*value) }),
	)
	if err != nil {
		return fmt.Errorf("upsert imported session: %w", err)
	}
	return nil
}

func messageInfoJSON(info session.MessageInfo) (string, error) {
	data, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("encode message info: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("decode message info: %w", err)
	}
	delete(raw, "id")
	delete(raw, "sessionID")
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode message data: %w", err)
	}
	return string(encoded), nil
}

func decodeMessageInfo(id string, sessionID string, _ int64, data string) (session.MessageInfo, error) {
	var info session.MessageInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return session.MessageInfo{}, fmt.Errorf("decode message data: %w", err)
	}
	info.ID = session.MessageID(id)
	info.SessionID = session.ID(sessionID)
	return info, nil
}

func partDataJSON(part session.Part) (string, error) {
	raw := map[string]any{}
	for key, value := range part.Data {
		raw[key] = value
	}
	raw["type"] = part.Type
	data, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode part data: %w", err)
	}
	return string(data), nil
}

func decodePart(id string, sessionID string, messageID string, data string) (session.Part, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return session.Part{}, fmt.Errorf("decode part data: %w", err)
	}
	partType, _ := raw["type"].(string)
	delete(raw, "type")
	return session.Part{
		ID:        session.PartID(id),
		SessionID: session.ID(sessionID),
		MessageID: session.MessageID(messageID),
		Type:      partType,
		Data:      raw,
	}, nil
}

type singleStringScanner interface {
	Scan(...any) error
}

func scanStatus(scanner singleStringScanner) (session.StatusInfo, error) {
	var raw string
	if err := scanner.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.StatusInfo{}, session.ErrNotFound
		}
		return session.StatusInfo{}, fmt.Errorf("scan session status: %w", err)
	}
	var status session.StatusInfo
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return session.StatusInfo{}, fmt.Errorf("decode session status: %w", err)
	}
	return status, nil
}

func scanDiff(scanner singleStringScanner) ([]map[string]any, error) {
	var raw string
	if err := scanner.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, session.ErrNotFound
		}
		return nil, fmt.Errorf("scan session diff: %w", err)
	}
	var diffs []map[string]any
	if err := json.Unmarshal([]byte(raw), &diffs); err != nil {
		return nil, fmt.Errorf("decode session diff: %w", err)
	}
	if diffs == nil {
		return []map[string]any{}, nil
	}
	return diffs, nil
}

type sessionScanner interface {
	Scan(...any) error
}

const sessionSelectColumns = `SELECT id, slug, project_id, workspace_id, directory, path, parent_id, title, agent, model, version,
cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
permission, time_created, time_updated, time_archived, time_compacting, share_url, summary_additions, summary_deletions, summary_files, summary_diffs, revert`

func scanSession(scanner sessionScanner) (session.Info, error) {
	var id string
	var slugValue string
	var projectID string
	var workspaceID sql.NullString
	var directory string
	var pathValue sql.NullString
	var parentID sql.NullString
	var title string
	var agent sql.NullString
	var modelJSON sql.NullString
	var version string
	var cost float64
	var tokensInput int
	var tokensOutput int
	var tokensReasoning int
	var tokensCacheRead int
	var tokensCacheWrite int
	var permissionJSON sql.NullString
	var created int64
	var updated int64
	var archived sql.NullInt64
	var compacting sql.NullInt64
	var shareURL sql.NullString
	var summaryAdditions sql.NullInt64
	var summaryDeletions sql.NullInt64
	var summaryFiles sql.NullInt64
	var summaryDiffs sql.NullString
	var revertJSON sql.NullString
	if err := scanner.Scan(
		&id,
		&slugValue,
		&projectID,
		&workspaceID,
		&directory,
		&pathValue,
		&parentID,
		&title,
		&agent,
		&modelJSON,
		&version,
		&cost,
		&tokensInput,
		&tokensOutput,
		&tokensReasoning,
		&tokensCacheRead,
		&tokensCacheWrite,
		&permissionJSON,
		&created,
		&updated,
		&archived,
		&compacting,
		&shareURL,
		&summaryAdditions,
		&summaryDeletions,
		&summaryFiles,
		&summaryDiffs,
		&revertJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Info{}, session.ErrNotFound
		}
		return session.Info{}, fmt.Errorf("scan session: %w", err)
	}

	info := session.Info{
		ID:        session.ID(id),
		Slug:      slugValue,
		ProjectID: projectID,
		Directory: directory,
		Title:     title,
		Agent:     agent.String,
		Version:   version,
		Cost:      cost,
		Tokens: &session.TokenUsage{
			Input:     tokensInput,
			Output:    tokensOutput,
			Reasoning: tokensReasoning,
			Cache: session.CacheUsage{
				Read:  tokensCacheRead,
				Write: tokensCacheWrite,
			},
		},
		Time: session.TimeInfo{
			Created: created,
			Updated: updated,
		},
	}
	if workspaceID.Valid {
		info.WorkspaceID = workspaceID.String
	}
	if pathValue.Valid {
		info.Path = pathValue.String
	}
	if parentID.Valid {
		parsed := session.ID(parentID.String)
		info.ParentID = &parsed
	}
	if modelJSON.Valid && modelJSON.String != "" {
		var model session.SessionModel
		if err := json.Unmarshal([]byte(modelJSON.String), &model); err != nil {
			return session.Info{}, fmt.Errorf("decode model: %w", err)
		}
		info.Model = &model
	}
	if archived.Valid {
		info.Time.Archived = &archived.Int64
	}
	if compacting.Valid {
		info.Time.Compacting = &compacting.Int64
	}
	if permissionJSON.Valid && permissionJSON.String != "" {
		if err := json.Unmarshal([]byte(permissionJSON.String), &info.Permission); err != nil {
			return session.Info{}, fmt.Errorf("decode permission: %w", err)
		}
	}
	if shareURL.Valid && shareURL.String != "" {
		info.Share = &session.ShareInfo{URL: shareURL.String}
	}
	if summaryAdditions.Valid || summaryDeletions.Valid || summaryFiles.Valid {
		info.Summary = &session.SummaryInfo{
			Additions: int(summaryAdditions.Int64),
			Deletions: int(summaryDeletions.Int64),
			Files:     int(summaryFiles.Int64),
		}
		if summaryDiffs.Valid && summaryDiffs.String != "" {
			if err := json.Unmarshal([]byte(summaryDiffs.String), &info.Summary.Diffs); err != nil {
				return session.Info{}, fmt.Errorf("decode summary diffs: %w", err)
			}
		}
	}
	if revertJSON.Valid && revertJSON.String != "" {
		var revert session.RevertInfo
		if err := json.Unmarshal([]byte(revertJSON.String), &revert); err != nil {
			return session.Info{}, fmt.Errorf("decode revert: %w", err)
		}
		info.Revert = &revert
	}
	return info, nil
}

func summaryDiffs(summary *session.SummaryInfo) []map[string]any {
	if summary == nil {
		return nil
	}
	return summary.Diffs
}

func shareURLFromInfo(share *session.ShareInfo) string {
	if share == nil {
		return ""
	}
	return share.URL
}

func optionalJSON(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func emptyTokens(value *session.TokenUsage) session.TokenUsage {
	if value == nil {
		return session.TokenUsage{Cache: session.CacheUsage{}}
	}
	return *value
}

func nullableInt[T any](value *T, pick func(*T) int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(pick(value)), Valid: true}
}

func nullableString(value string, valid bool) sql.NullString {
	if !valid {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func slug(title string) string {
	value := strings.ToLower(strings.TrimSpace(title))
	if value == "" {
		return "untitled"
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r < 'a' || r > 'z' && (r < '0' || r > '9')
	})
	if len(fields) == 0 {
		return "untitled"
	}
	return strings.Join(fields, "-")
}

const projectSchema = `CREATE TABLE IF NOT EXISTS project (
id text PRIMARY KEY,
worktree text NOT NULL,
vcs text,
name text,
icon_url text,
icon_url_override text,
icon_color text,
time_created integer NOT NULL,
time_updated integer NOT NULL,
time_initialized integer,
sandboxes text NOT NULL,
commands text
)`

const sessionSchema = `CREATE TABLE IF NOT EXISTS session (
id text PRIMARY KEY,
project_id text NOT NULL,
workspace_id text,
parent_id text,
slug text NOT NULL,
directory text NOT NULL,
path text,
title text NOT NULL,
version text NOT NULL,
share_url text,
summary_additions integer,
summary_deletions integer,
summary_files integer,
summary_diffs text,
cost real NOT NULL DEFAULT 0,
tokens_input integer NOT NULL DEFAULT 0,
tokens_output integer NOT NULL DEFAULT 0,
tokens_reasoning integer NOT NULL DEFAULT 0,
tokens_cache_read integer NOT NULL DEFAULT 0,
tokens_cache_write integer NOT NULL DEFAULT 0,
revert text,
permission text,
agent text,
model text,
time_created integer NOT NULL,
time_updated integer NOT NULL,
time_compacting integer,
time_archived integer,
CONSTRAINT fk_session_project_id_project_id_fk FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE
)`

const messageSchema = `CREATE TABLE IF NOT EXISTS message (
id text PRIMARY KEY,
session_id text NOT NULL,
time_created integer NOT NULL,
time_updated integer NOT NULL,
data text NOT NULL,
CONSTRAINT fk_message_session_id_session_id_fk FOREIGN KEY (session_id) REFERENCES session(id) ON DELETE CASCADE
)`

const partSchema = `CREATE TABLE IF NOT EXISTS part (
id text PRIMARY KEY,
message_id text NOT NULL,
session_id text NOT NULL,
time_created integer NOT NULL,
time_updated integer NOT NULL,
data text NOT NULL,
CONSTRAINT fk_part_message_id_message_id_fk FOREIGN KEY (message_id) REFERENCES message(id) ON DELETE CASCADE
)`

const todoSchema = `CREATE TABLE IF NOT EXISTS todo (
session_id text NOT NULL,
content text NOT NULL,
status text NOT NULL,
priority text NOT NULL,
position integer NOT NULL,
time_created integer NOT NULL,
time_updated integer NOT NULL,
PRIMARY KEY (session_id, position),
CONSTRAINT fk_todo_session_id_session_id_fk FOREIGN KEY (session_id) REFERENCES session(id) ON DELETE CASCADE
)`

const sessionDiffSchema = `CREATE TABLE IF NOT EXISTS session_diff (
session_id text NOT NULL PRIMARY KEY,
data text NOT NULL,
time_updated integer NOT NULL,
CONSTRAINT fk_session_diff_session_id_session_id_fk FOREIGN KEY (session_id) REFERENCES session(id) ON DELETE CASCADE
)`

const sessionStatusSchema = `CREATE TABLE IF NOT EXISTS session_status (
session_id text NOT NULL PRIMARY KEY,
data text NOT NULL,
time_updated integer NOT NULL,
CONSTRAINT fk_session_status_session_id_session_id_fk FOREIGN KEY (session_id) REFERENCES session(id) ON DELETE CASCADE
)`

const eventSequenceSchema = `CREATE TABLE IF NOT EXISTS event_sequence (
aggregate_id text NOT NULL PRIMARY KEY,
seq integer NOT NULL,
owner_id text
)`

const eventSchema = `CREATE TABLE IF NOT EXISTS event (
id text PRIMARY KEY,
aggregate_id text NOT NULL,
seq integer NOT NULL,
type text NOT NULL,
data text NOT NULL,
CONSTRAINT fk_event_aggregate_id_event_sequence_aggregate_id_fk FOREIGN KEY (aggregate_id) REFERENCES event_sequence(aggregate_id) ON DELETE CASCADE
)`
