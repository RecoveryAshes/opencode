package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
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
	query := `SELECT id, parent_id, title, permission, time_created, time_updated, time_archived
FROM session`
	args := []any{}
	if filter.Search != "" {
		query += " WHERE lower(title) LIKE ?"
		args = append(args, "%"+strings.ToLower(filter.Search)+"%")
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
	if err := store.ensureProject(ctx, now); err != nil {
		return session.Info{}, err
	}

	info := session.Info{
		ID:       id,
		ParentID: input.ParentID,
		Title:    input.Title,
		Time: session.TimeInfo{
			Created: now,
			Updated: now,
		},
	}
	parentID := sql.NullString{}
	if input.ParentID != nil {
		parentID = sql.NullString{String: string(*input.ParentID), Valid: true}
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO session (
id, project_id, parent_id, slug, directory, title, version, permission, time_created, time_updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		defaultProjectID,
		parentID,
		slug(input.Title),
		"",
		input.Title,
		"go-migration",
		"[]",
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
	row := store.db.QueryRowContext(ctx, `SELECT id, parent_id, title, permission, time_created, time_updated, time_archived
FROM session WHERE id = ?`, id)
	return scanSession(row)
}

// Update changes mutable session fields.
func (store *SQLiteSessionStore) Update(ctx context.Context, id session.ID, input session.UpdateInput) (session.Info, error) {
	if input.Title != nil {
		result, err := store.db.ExecContext(ctx, `UPDATE session SET title = ?, slug = ?, time_updated = ? WHERE id = ?`,
			*input.Title,
			slug(*input.Title),
			session.NowMillis(),
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
	}
	return store.Get(ctx, id)
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

func (store *SQLiteSessionStore) configure(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA cache_size = -64000`,
		`PRAGMA foreign_keys = ON`,
		projectSchema,
		sessionSchema,
		`CREATE INDEX IF NOT EXISTS session_project_idx ON session(project_id)`,
		`CREATE INDEX IF NOT EXISTS session_workspace_idx ON session(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS session_parent_idx ON session(parent_id)`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (store *SQLiteSessionStore) ensureProject(ctx context.Context, now int64) error {
	_, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO project (
id, worktree, name, time_created, time_updated, sandboxes
) VALUES (?, ?, ?, ?, ?, ?)`, defaultProjectID, filepath.Clean("."), "Go Migration", now, now, "[]")
	if err != nil {
		return fmt.Errorf("ensure project: %w", err)
	}
	return nil
}

type sessionScanner interface {
	Scan(...any) error
}

func scanSession(scanner sessionScanner) (session.Info, error) {
	var id string
	var parentID sql.NullString
	var title string
	var permissionJSON sql.NullString
	var created int64
	var updated int64
	var archived sql.NullInt64
	if err := scanner.Scan(&id, &parentID, &title, &permissionJSON, &created, &updated, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Info{}, session.ErrNotFound
		}
		return session.Info{}, fmt.Errorf("scan session: %w", err)
	}

	info := session.Info{
		ID:    session.ID(id),
		Title: title,
		Time: session.TimeInfo{
			Created: created,
			Updated: updated,
		},
	}
	if parentID.Valid {
		parsed := session.ID(parentID.String)
		info.ParentID = &parsed
	}
	if archived.Valid {
		info.Time.Archived = &archived.Int64
	}
	if permissionJSON.Valid && permissionJSON.String != "" {
		if err := json.Unmarshal([]byte(permissionJSON.String), &info.Permission); err != nil {
			return session.Info{}, fmt.Errorf("decode permission: %w", err)
		}
	}
	return info, nil
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
