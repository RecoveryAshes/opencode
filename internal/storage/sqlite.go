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
	query := `SELECT id, parent_id, title, permission, time_created, time_updated, time_archived, share_url, summary_additions, summary_deletions, summary_files, summary_diffs, revert
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
	row := store.db.QueryRowContext(ctx, `SELECT id, parent_id, title, permission, time_created, time_updated, time_archived, share_url, summary_additions, summary_deletions, summary_files, summary_diffs, revert
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
	}
	if input.Archived != nil {
		current.Time.Archived = input.Archived
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
	shareURL := sql.NullString{}
	if current.Share != nil && current.Share.URL != "" {
		shareURL = sql.NullString{String: current.Share.URL, Valid: true}
	}
	archived := sql.NullInt64{}
	if current.Time.Archived != nil {
		archived = sql.NullInt64{Int64: *current.Time.Archived, Valid: true}
	}
	summary := current.Summary
	result, err := store.db.ExecContext(ctx, `UPDATE session SET
title = ?, slug = ?, permission = ?, time_updated = ?, time_archived = ?,
share_url = ?, summary_additions = ?, summary_deletions = ?, summary_files = ?, summary_diffs = ?, revert = ?
WHERE id = ?`,
		current.Title,
		slug(current.Title),
		string(permissionJSON),
		session.NowMillis(),
		archived,
		shareURL,
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Additions }),
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Deletions }),
		nullableInt(summary, func(value *session.SummaryInfo) int { return value.Files }),
		nullableString(string(summaryDiffs), summary != nil && len(summary.Diffs) > 0),
		nullableString(revertJSON, current.Revert != nil),
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
	rows, err := store.db.QueryContext(ctx, `SELECT id, parent_id, title, permission, time_created, time_updated, time_archived, share_url, summary_additions, summary_deletions, summary_files, summary_diffs, revert
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
	child, err := store.Create(ctx, session.CreateInput{Title: parent.Title, ParentID: &parentID})
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
		`CREATE INDEX IF NOT EXISTS session_project_idx ON session(project_id)`,
		`CREATE INDEX IF NOT EXISTS session_workspace_idx ON session(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS session_parent_idx ON session(parent_id)`,
		`CREATE INDEX IF NOT EXISTS message_session_time_created_id_idx ON message(session_id, time_created, id)`,
		`CREATE INDEX IF NOT EXISTS part_message_id_id_idx ON part(message_id, id)`,
		`CREATE INDEX IF NOT EXISTS part_session_idx ON part(session_id)`,
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

// Messages returns messages in creation order.
func (store *SQLiteSessionStore) Messages(ctx context.Context, sessionID session.ID, limit int) ([]session.WithParts, error) {
	if _, err := store.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ? ORDER BY time_created ASC, id ASC`
	args := []any{sessionID}
	if limit > 0 {
		query = `SELECT id, session_id, time_created, time_updated, data FROM (
SELECT id, session_id, time_created, time_updated, data FROM message WHERE session_id = ? ORDER BY time_created DESC, id DESC LIMIT ?
) ORDER BY time_created ASC, id ASC`
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
	var shareURL sql.NullString
	var summaryAdditions sql.NullInt64
	var summaryDeletions sql.NullInt64
	var summaryFiles sql.NullInt64
	var summaryDiffs sql.NullString
	var revertJSON sql.NullString
	if err := scanner.Scan(
		&id,
		&parentID,
		&title,
		&permissionJSON,
		&created,
		&updated,
		&archived,
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
