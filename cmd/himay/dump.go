package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/usage"
)

func dump(ctx context.Context, st *store.Store, sessionID string, withProvenance bool, w io.Writer) error {
	ss, err := st.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	msgs, err := trace.LoadAll(ctx, st, sessionID)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "session  %s  %q\n", ss.ID, ss.Title)
	fmt.Fprintf(w, "dir      %s   version %s\n", ss.Directory, ss.Version)
	fmt.Fprintf(w, "default  agent=%s model=%s  (session-level; per-step values below)\n", orDash(ss.Agent), ss.Model)
	if ss.ParentID != "" {
		fmt.Fprintf(w, "parent   %s\n", ss.ParentID)
	}
	if ss.Children > 0 {
		fmt.Fprintf(w, "children %d subagent sessions\n", ss.Children)
	}
	rep := usage.Build(msgs)
	fmt.Fprintf(w, "usage    %d steps · %s tokens · $%.4f reported · %d errors\n",
		rep.Total.Steps, trace.Count(rep.Total.Tokens.Total()), rep.Total.Cost, rep.Total.Errors)
	for _, l := range rep.ByModel {
		fmt.Fprintf(w, "  %-44s %4d steps  %8s tok  $%.4f\n", l.Key, l.Steps, trace.Count(l.Tokens.Total()), l.Cost)
	}
	fmt.Fprintln(w)
	start := ss.Created
	if len(msgs) > 0 && !msgs[0].Created.IsZero() {
		start = msgs[0].Created
	}
	if start.IsZero() {
		start = time.Now()
	}
	if err := trace.WritePlain(w, trace.Build(msgs), start); err != nil {
		return err
	}
	if !withProvenance {
		return nil
	}
	return dumpProvenance(ctx, st, ss, msgs, w)
}

func dumpProvenance(ctx context.Context, st *store.Store, ss model.SessionSummary, msgs []model.Message, w io.Writer) error {
	srcs, err := provenance.Extract(ctx, st, ss.ID, msgs)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "\ninstruction provenance")
	if len(srcs) == 0 {
		fmt.Fprintf(w, "  OpenCode stored no instruction records for this session (v%s).\n", ss.Version)
		return nil
	}
	steps, err := st.StepTexts(ctx, ss.ID, -1)
	if err != nil {
		return err
	}
	for i := range srcs {
		s := &srcs[i]
		if err := provenance.Load(ctx, st, s); err != nil {
			return err
		}
		label := s.Name
		if label == "" {
			label = s.Path
		}
		if label == "" {
			label = s.Key
		}
		fmt.Fprintf(w, "  [%s] %-24s seq=%-5d %s  — %s\n", s.Evidence, s.Kind, s.Seq, label, s.Source)
		ms, err := provenance.Correlate(ctx, *s, steps)
		if err != nil {
			return err
		}
		for _, m := range ms {
			fmt.Fprintf(w, "      [%s] seq=%d %s %s: %s ⇐ %s\n", m.Evidence, m.Seq, m.Kind, m.Tool, m.Rule, m.Instr)
		}
	}
	fmt.Fprintln(w, "  "+provenance.Disclaimer)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
