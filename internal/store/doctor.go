package store

import (
	"context"
	"fmt"
	"sort"
)

// KnownMessageTypes are the session_message types Himay renders specifically.
var KnownMessageTypes = map[string]bool{
	"assistant": true, "user": true, "system": true, "synthetic": true, "compaction": true,
	"idle": true, "model-switched": true, "agent-switched": true, "location-switched": true,
	"skill": true, "shell": true,
}

// Stats is a diagnostic snapshot for `himay doctor`. It runs full-table
// aggregates, so it is never called on TUI startup.
type Stats struct {
	Path            string
	JournalMode     string
	Tables          []string
	Sessions        int64
	ChildSessions   int64
	Messages        int64
	TypeCounts      map[string]int64
	UnknownTypes    []string
	InvalidJSON     int64
	InstructionRows int64
	LegacyGapMsgs   int64
	LegacyGapSess   int64
	Migration       string
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{Path: s.path, JournalMode: s.Caps.JournalMode, TypeCounts: map[string]int64{}}
	for t := range s.Caps.Tables {
		st.Tables = append(st.Tables, t)
	}
	sort.Strings(st.Tables)
	q := func(dst *int64, sql string) error {
		if err := s.db.QueryRowContext(ctx, sql).Scan(dst); err != nil {
			return fmt.Errorf("%s: %w", sql, err)
		}
		return nil
	}
	if err := q(&st.Sessions, `SELECT count(*) FROM session_v2`); err != nil {
		return nil, err
	}
	if err := q(&st.ChildSessions, `SELECT count(*) FROM session_v2 WHERE parent_id IS NOT NULL`); err != nil {
		return nil, err
	}
	if err := q(&st.InvalidJSON, `SELECT count(*) FROM session_message WHERE NOT json_valid(data)`); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT type, count(*) FROM session_message GROUP BY type ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		var n int64
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st.TypeCounts[t] = n
		st.Messages += n
		if !KnownMessageTypes[t] {
			st.UnknownTypes = append(st.UnknownTypes, t)
		}
	}
	rows.Close()
	if s.Caps.HasTable("instruction_state") {
		if err := q(&st.InstructionRows, `SELECT count(*) FROM instruction_state`); err != nil {
			return nil, err
		}
	}
	if s.Caps.HasTable("message") {
		// Read-only transparency stat: legacy messages with no v2 counterpart.
		// Himay v1 does not display these.
		if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT session_id) FROM message m
			WHERE NOT EXISTS (SELECT 1 FROM session_message sm WHERE sm.id = m.id)`).
			Scan(&st.LegacyGapMsgs, &st.LegacyGapSess); err != nil {
			return nil, err
		}
	}
	if s.Caps.HasTable("kv") {
		_ = s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = 'migration.v1-v2'`).Scan(&st.Migration)
	}
	return st, nil
}
