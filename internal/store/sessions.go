package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0xGurg/himay/internal/model"
)

func msTime(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}

func parseModel(s sql.NullString) model.ModelRef {
	var m model.ModelRef
	if s.Valid && s.String != "" {
		_ = json.Unmarshal([]byte(s.String), &m)
	}
	return m
}

// Projects lists projects that have at least one root session, most recent first.
func (s *Store) Projects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.worktree, COALESCE(p.name,''), count(s.id), max(s.time_updated)
		FROM project p JOIN session_v2 s ON s.project_id = p.id AND s.parent_id IS NULL
		GROUP BY p.id ORDER BY 5 DESC`)
	if err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		var p model.Project
		var last sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Worktree, &p.Name, &p.Sessions, &last); err != nil {
			return nil, err
		}
		p.LastActive = msTime(last)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) sessionCols() string {
	t := "session_v2"
	return fmt.Sprintf(`s.id, s.project_id, COALESCE(s.parent_id,''), COALESCE(s.title,''), s.directory,
		COALESCE(%s,''), s.version, %s, s.cost, s.tokens_input, s.tokens_output, s.tokens_reasoning,
		s.tokens_cache_read, s.tokens_cache_write, s.time_created, s.time_updated, %s,
		(SELECT count(*) FROM session_v2 c WHERE c.parent_id = s.id)`,
		s.col(t, "agent"), s.col(t, "model"), s.col(t, "time_archived"))
}

func scanSession(sc interface{ Scan(...any) error }) (model.SessionSummary, error) {
	var ss model.SessionSummary
	var mdl sql.NullString
	var created, updated, archived sql.NullInt64
	err := sc.Scan(&ss.ID, &ss.ProjectID, &ss.ParentID, &ss.Title, &ss.Directory, &ss.Agent, &ss.Version,
		&mdl, &ss.Cost, &ss.Tokens.Input, &ss.Tokens.Output, &ss.Tokens.Reasoning,
		&ss.Tokens.CacheRead, &ss.Tokens.CacheWrite, &created, &updated, &archived, &ss.Children)
	if err != nil {
		return ss, err
	}
	ss.Model = parseModel(mdl)
	ss.Tokens.Known = ss.Tokens.Total() > 0
	ss.Created, ss.Updated = msTime(created), msTime(updated)
	if archived.Valid {
		t := msTime(archived)
		ss.Archived = &t
	}
	return ss, nil
}

// Cursor is a keyset position for session paging (time_updated DESC, id DESC).
type Cursor struct {
	Updated int64
	ID      string
}

// RootSessions pages root (non-subagent) sessions of a project. A zero cursor starts at the top.
func (s *Store) RootSessions(ctx context.Context, projectID string, after Cursor, limit int, includeArchived bool) ([]model.SessionSummary, error) {
	q := `SELECT ` + s.sessionCols() + ` FROM session_v2 s
		WHERE s.project_id = ? AND s.parent_id IS NULL`
	args := []any{projectID}
	if !includeArchived && s.Caps.Has("session_v2", "time_archived") {
		q += ` AND s.time_archived IS NULL`
	}
	if after.ID != "" {
		q += ` AND (s.time_updated < ? OR (s.time_updated = ? AND s.id < ?))`
		args = append(args, after.Updated, after.Updated, after.ID)
	}
	q += ` ORDER BY s.time_updated DESC, s.id DESC LIMIT ?`
	args = append(args, limit)
	return s.querySessions(ctx, q, args...)
}

// Children lists direct subagent sessions of a session, oldest first.
func (s *Store) Children(ctx context.Context, parentID string) ([]model.SessionSummary, error) {
	return s.querySessions(ctx, `SELECT `+s.sessionCols()+` FROM session_v2 s
		WHERE s.parent_id = ? ORDER BY s.time_created, s.id`, parentID)
}

// Session loads one session by ID.
func (s *Store) Session(ctx context.Context, id string) (model.SessionSummary, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+s.sessionCols()+` FROM session_v2 s WHERE s.id = ?`, id)
	ss, err := scanSession(row)
	if err == sql.ErrNoRows {
		return ss, fmt.Errorf("session %s not found", id)
	}
	return ss, err
}

// SearchTitles finds sessions (root or child) whose title contains q.
func (s *Store) SearchTitles(ctx context.Context, q string, limit int) ([]model.SessionSummary, error) {
	return s.querySessions(ctx, `SELECT `+s.sessionCols()+` FROM session_v2 s
		WHERE s.title LIKE '%' || ? || '%' ESCAPE '\' ORDER BY s.time_updated DESC LIMIT ?`,
		escapeLike(q), limit)
}

func (s *Store) querySessions(ctx context.Context, q string, args ...any) ([]model.SessionSummary, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	defer rows.Close()
	var out []model.SessionSummary
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%', '_', '\\':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
