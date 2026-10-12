package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// InstructionState is the instruction_state row for a session.
type InstructionState struct {
	EpochStart, ThroughSeq int64
	Initial, Current       map[string]string // section key → blob hash or "removed"
}

type InstructionEntry struct {
	Key, Value string
	Removed    bool
	Created    int64
}

// Instructions loads instruction_state and instruction_entry for a session.
// Either may be absent; older OpenCode versions recorded neither.
func (s *Store) Instructions(ctx context.Context, sessionID string) (*InstructionState, []InstructionEntry, error) {
	var st *InstructionState
	if s.Caps.HasTable("instruction_state") {
		var init, cur string
		var ep, th int64
		err := s.db.QueryRowContext(ctx, `SELECT epoch_start, through_seq, initial_values, current_values
			FROM instruction_state WHERE session_id = ?`, sessionID).Scan(&ep, &th, &init, &cur)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return nil, nil, fmt.Errorf("instruction_state: %w", err)
		default:
			st = &InstructionState{EpochStart: ep, ThroughSeq: th}
			_ = json.Unmarshal([]byte(init), &st.Initial) // tolerate malformed: leave nil
			_ = json.Unmarshal([]byte(cur), &st.Current)
		}
	}
	var entries []InstructionEntry
	if s.Caps.HasTable("instruction_entry") {
		rows, err := s.db.QueryContext(ctx, `SELECT key, COALESCE(substr(value,1,4000),''), removed, time_created
			FROM instruction_entry WHERE session_id = ? ORDER BY time_created, key`, sessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("instruction_entry: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e InstructionEntry
			if err := rows.Scan(&e.Key, &e.Value, &e.Removed, &e.Created); err != nil {
				return nil, nil, err
			}
			entries = append(entries, e)
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return st, entries, nil
}

// InstructionBlob returns a blob's decoded text. Blob values are JSON-encoded
// strings or objects; strings are unwrapped, objects returned as JSON.
func (s *Store) InstructionBlob(ctx context.Context, hash string) (string, error) {
	if !s.Caps.HasTable("instruction_blob") {
		return "", nil
	}
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM instruction_blob WHERE hash = ?`, hash).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("instruction_blob: %w", err)
	}
	var str string
	if json.Unmarshal([]byte(v.String), &str) == nil {
		return str, nil
	}
	return v.String, nil
}
