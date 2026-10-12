package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Hit is one deep-search match.
type Hit struct {
	SessionID, MsgID, Type string
	Seq                    int64
	Snippet                string
}

// SearchSession finds messages in one session whose raw JSON contains needle
// (case-insensitive for ASCII). Indexed by session_id, so it is cheap.
func (s *Store) SearchSession(ctx context.Context, sessionID, needle string, limit int) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, id, type, seq,
			substr(data, max(1, instr(lower(data), lower(?1)) - 60), 160)
		FROM session_message
		WHERE session_id = ?2 AND instr(lower(data), lower(?1)) > 0
		ORDER BY seq LIMIT ?3`, needle, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("search session: %w", err)
	}
	return scanHits(rows)
}

// DeepSearchChunk scans one rowid range of session_message for needle. Callers
// iterate chunks so a global scan can report progress and stop on cancel.
// It returns the hits and the highest rowid examined (0 when exhausted).
func (s *Store) DeepSearchChunk(ctx context.Context, needle string, afterRowID int64, chunk int) ([]Hit, int64, error) {
	var hi sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(rowid) FROM (SELECT rowid FROM session_message
		WHERE rowid > ? ORDER BY rowid LIMIT ?)`, afterRowID, chunk).Scan(&hi)
	if err != nil {
		return nil, 0, err
	}
	if !hi.Valid {
		return nil, 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, id, type, seq,
			substr(data, max(1, instr(lower(data), lower(?1)) - 60), 160)
		FROM session_message
		WHERE rowid > ?2 AND rowid <= ?3 AND instr(lower(data), lower(?1)) > 0
		ORDER BY rowid`, needle, afterRowID, hi.Int64)
	if err != nil {
		return nil, 0, fmt.Errorf("deep search: %w", err)
	}
	hits, err := scanHits(rows)
	return hits, hi.Int64, err
}

// MaxRowID is used to compute deep-search progress.
func (s *Store) MaxRowID(ctx context.Context) (int64, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(rowid) FROM session_message`).Scan(&v)
	return v.Int64, err
}

func scanHits(rows *sql.Rows) ([]Hit, error) {
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SessionID, &h.MsgID, &h.Type, &h.Seq, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
