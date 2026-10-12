// Command himay is a read-only terminal viewer for OpenCode session traces.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"time"

	"github.com/0xGurg/himay/internal/store"
)

const usageText = `himay (ᜑᜒᜋᜌ᜔) — read-only OpenCode session trace viewer

Usage:
  himay [flags]                   open the TUI
  himay --web [--addr host:port]  serve the browser UI on localhost
  himay doctor [flags]            print database diagnostics
  himay dump --session ID [flags] print a session trace as text

Flags:
`

func main() {
	fs := flag.NewFlagSet("himay", flag.ExitOnError)
	dbPath := fs.String("db", store.DefaultPath(), "path to opencode.db (opened read-only)")
	session := fs.String("session", "", "session ID (dump; or open directly in the TUI)")
	webMode := fs.Bool("web", false, "serve the read-only browser UI instead of the TUI")
	addr := fs.String("addr", "127.0.0.1:7878", "web: listen address (loopback only)")
	noOpen := fs.Bool("no-open", false, "web: do not open a browser")
	prov := fs.Bool("provenance", false, "dump: include instruction provenance and correlations")
	ascii := fs.Bool("ascii", os.Getenv("HIMAY_ASCII") != "", "use ASCII instead of the Baybayin title")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText); fs.PrintDefaults() }

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && (args[0] == "doctor" || args[0] == "dump") {
		cmd, args = args[0], args[1:]
	}
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(openCtx, *dbPath)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "himay:", err)
		os.Exit(1)
	}
	defer st.Close()

	switch cmd {
	case "doctor":
		err = doctor(ctx, st)
	case "dump":
		if *session == "" {
			err = fmt.Errorf("dump requires --session")
			break
		}
		err = dump(ctx, st, *session, *prov, os.Stdout)
	default:
		if *webMode {
			err = runWeb(ctx, st, *addr, *session, *ascii, !*noOpen)
			break
		}
		err = runTUI(ctx, st, *session, *ascii)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "himay:", err)
		os.Exit(1)
	}
}

func doctor(ctx context.Context, st *store.Store) error {
	s, err := st.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("database      %s (read-only, journal=%s)\n", s.Path, s.JournalMode)
	fmt.Printf("tables        %v\n", s.Tables)
	fmt.Printf("sessions      %d (%d subagent)\n", s.Sessions, s.ChildSessions)
	fmt.Printf("messages      %d (invalid JSON: %d)\n", s.Messages, s.InvalidJSON)
	types := make([]string, 0, len(s.TypeCounts))
	for t := range s.TypeCounts {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return s.TypeCounts[types[i]] > s.TypeCounts[types[j]] })
	for _, t := range types {
		mark := ""
		if !store.KnownMessageTypes[t] {
			mark = "  (unknown to himay — shown as raw rows)"
		}
		fmt.Printf("  %-18s %7d%s\n", t, s.TypeCounts[t], mark)
	}
	fmt.Printf("instructions  %d sessions with instruction_state\n", s.InstructionRows)
	if s.Migration != "" {
		fmt.Printf("migration     v1→v2 %s\n", s.Migration)
	}
	if s.LegacyGapMsgs > 0 {
		fmt.Printf("legacy gap    %d messages in %d sessions exist only in legacy tables (not shown by himay v1)\n",
			s.LegacyGapMsgs, s.LegacyGapSess)
	}
	return nil
}
