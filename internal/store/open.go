// Package store is Himay's only gateway to OpenCode's SQLite database.
// Every connection is opened read-only; the package exposes no write paths.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DefaultPath returns OpenCode's default database location.
func DefaultPath() string {
	if p := os.Getenv("HIMAY_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "opencode.db")
}

// Store wraps a read-only *sql.DB plus the capabilities detected at open.
type Store struct {
	db   *sql.DB
	path string
	Caps Caps
}

// DSN builds a read-only URI. immutable=1 is deliberately NOT used: it would
// ignore the WAL and show stale or inconsistent data while OpenCode runs.
func DSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(3000)")
	u.RawQuery = q.Encode()
	return u.String()
}

// Open opens the database read-only and probes its schema.
func Open(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("opencode database not found at %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	s := &Store{db: db, path: path}
	if err := s.probe(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if !s.Caps.HasTable("session_v2") || !s.Caps.HasTable("session_message") {
		db.Close()
		return nil, errors.New("database has no session_v2/session_message tables; Himay requires OpenCode ≥ 1.3 storage")
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

// Caps records which tables and columns exist, so queries can degrade gracefully.
type Caps struct {
	Tables      map[string]map[string]bool
	JournalMode string
}

func (c Caps) HasTable(t string) bool { _, ok := c.Tables[t]; return ok }
func (c Caps) Has(t, col string) bool { return c.Tables[t][col] }

var probedTables = []string{
	"project", "session_v2", "session_message", "session_pending", "session_inbox",
	"instruction_state", "instruction_entry", "instruction_blob",
	"session", "message", "kv",
}

func (s *Store) probe(ctx context.Context) error {
	s.Caps.Tables = map[string]map[string]bool{}
	for _, t := range probedTables {
		rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", t)
		if err != nil {
			return fmt.Errorf("probe %s: %w", t, err)
		}
		cols := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			cols[n] = true
		}
		rows.Close()
		if len(cols) > 0 {
			s.Caps.Tables[t] = cols
		}
	}
	_ = s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&s.Caps.JournalMode)
	return nil
}

// col returns the column name if it exists, else a NULL literal, so optional
// columns can be selected uniformly.
func (s *Store) col(table, column string) string {
	if s.Caps.Has(table, column) {
		return column
	}
	return "NULL"
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
