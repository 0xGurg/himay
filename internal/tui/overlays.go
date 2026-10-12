package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/textutil"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/usage"
	"github.com/0xGurg/himay/internal/view"
)

// ---------- filter ----------

type filterGroup struct {
	name  string
	kinds []trace.Kind
}

var filterGroups = []filterGroup{
	{"user requests", []trace.Kind{trace.KUser}},
	{"steps", []trace.Kind{trace.KStep}},
	{"text", []trace.Kind{trace.KText}},
	{"reasoning", []trace.Kind{trace.KReasoning, trace.KReasoningUnavailable}},
	{"tools", []trace.Kind{trace.KTool, trace.KShell}},
	{"subagents", []trace.Kind{trace.KSubagent}},
	{"file changes", []trace.Kind{trace.KFileChange}},
	{"errors & retries", []trace.Kind{trace.KError, trace.KRetry}},
	{"switches", []trace.Kind{trace.KModelSwitched, trace.KAgentSwitched, trace.KLocationSwitched}},
	{"instructions & skills", []trace.Kind{trace.KInstructionLoaded, trace.KInstructionUpdated, trace.KSkill, trace.KToolsChanged}},
	{"system / synthetic", []trace.Kind{trace.KSystem, trace.KSynthetic}},
	{"compaction & idle", []trace.Kind{trace.KCompaction, trace.KIdle}},
	{"unknown", []trace.Kind{trace.KUnknown}},
}

type filterState struct {
	groups     map[int]bool
	failedOnly bool
	agent      string
	model      string
}

func (f filterState) active() bool {
	return len(f.groups) > 0 || f.failedOnly || f.agent != "" || f.model != ""
}

func (f filterState) match(r trace.Row) bool {
	if f.failedOnly && r.Status != trace.SFail && r.Kind != trace.KError {
		return false
	}
	if f.agent != "" && r.Agent != f.agent {
		return false
	}
	if f.model != "" && r.Model.ID != f.model {
		return false
	}
	if len(f.groups) == 0 {
		return true
	}
	for gi := range f.groups {
		for _, k := range filterGroups[gi].kinds {
			if r.Kind == k {
				return true
			}
		}
	}
	return false
}

func (f filterState) describe() string {
	var p []string
	for gi := range f.groups {
		p = append(p, filterGroups[gi].name)
	}
	sort.Strings(p)
	if f.failedOnly {
		p = append(p, "failed")
	}
	if f.agent != "" {
		p = append(p, "agent="+f.agent)
	}
	if f.model != "" {
		p = append(p, "model="+f.model)
	}
	return strings.Join(p, ", ")
}

// filterItems lists selectable options: kind groups, failed-only, agents, models.
func (s *sessionScreen) filterItems() []string {
	items := make([]string, 0, len(filterGroups)+8)
	for _, g := range filterGroups {
		items = append(items, "kind:"+g.name)
	}
	items = append(items, "failed only")
	agents, models := map[string]bool{}, map[string]bool{}
	for _, m := range s.msgs {
		if m.Type == "assistant" {
			if m.Agent != "" {
				agents[m.Agent] = true
			}
			if m.Model.ID != "" {
				models[m.Model.ID] = true
			}
		}
	}
	for _, a := range sortedKeys(agents) {
		items = append(items, "agent:"+a)
	}
	for _, m := range sortedKeys(models) {
		items = append(items, "model:"+m)
	}
	return items
}

func (s *sessionScreen) filterKey(a *App, k string) tea.Cmd {
	items := s.filterItems()
	if s.ov.move(k, len(items), a.h-8) {
		return nil
	}
	switch k {
	case "space", "enter", "x":
		it := items[s.ov.pos]
		switch {
		case strings.HasPrefix(it, "kind:"):
			if s.filter.groups == nil {
				s.filter.groups = map[int]bool{}
			}
			gi := s.ov.pos
			if s.filter.groups[gi] {
				delete(s.filter.groups, gi)
			} else {
				s.filter.groups[gi] = true
			}
		case it == "failed only":
			s.filter.failedOnly = !s.filter.failedOnly
		case strings.HasPrefix(it, "agent:"):
			v := strings.TrimPrefix(it, "agent:")
			if s.filter.agent == v {
				v = ""
			}
			s.filter.agent = v
		case strings.HasPrefix(it, "model:"):
			v := strings.TrimPrefix(it, "model:")
			if s.filter.model == v {
				v = ""
			}
			s.filter.model = v
		}
		s.cur = cursor{}
		s.recompute()
	case "c":
		s.filter = filterState{}
		s.recompute()
	}
	return nil
}

func (s *sessionScreen) filterView(a *App, w, h int) string {
	items := s.filterItems()
	head := a.s.dim.Render("space toggles · c clears · esc closes — kinds are OR'ed; failed/agent/model narrow") + "\n"
	return head + s.ov.window(len(items), h-1, func(i int, sel bool) string {
		it := items[i]
		on := false
		switch {
		case strings.HasPrefix(it, "kind:"):
			on = s.filter.groups[i]
		case it == "failed only":
			on = s.filter.failedOnly
		case strings.HasPrefix(it, "agent:"):
			on = s.filter.agent == strings.TrimPrefix(it, "agent:")
		case strings.HasPrefix(it, "model:"):
			on = s.filter.model == strings.TrimPrefix(it, "model:")
		}
		box := "[ ] "
		if on {
			box = a.s.ok.Render("[x] ")
		}
		line := " " + box + it
		if sel {
			return a.s.sel.Render(textutil.Pad(textutil.Sanitize(line), w))
		}
		return line
	})
}

// ---------- usage ----------

func (s *sessionScreen) usageView(a *App, w int) string {
	rep := usage.Build(s.msgs)
	var b strings.Builder
	note := "Per-step values from each assistant message — not the session default model."
	if s.more {
		note += " Partial: more messages not loaded yet (G loads all)."
	}
	b.WriteString(a.s.dim.Render(note) + "\n")
	b.WriteString(a.s.dim.Render("Cost is what OpenCode recorded; subscription providers often report $0.") + "\n\n")
	table := func(title string, lines []usage.Line) {
		b.WriteString(a.s.bold.Render(title) + "\n")
		kw := min(46, max(20, w-60))
		fmt.Fprintf(&b, "  %s %6s %9s %9s %9s %9s %10s %4s\n", textutil.Pad("", kw), "steps", "input", "output", "reason", "cache r", "cost", "err")
		for _, l := range lines {
			cost := "—"
			if l.CostKnown {
				cost = fmt.Sprintf("$%.4f", l.Cost)
			}
			tok := func(v int64) string {
				if !l.Tokens.Known {
					return "—"
				}
				return trace.Count(v)
			}
			errS := fmt.Sprint(l.Errors)
			if l.Errors > 0 {
				errS = a.s.fail.Render(errS)
			}
			fmt.Fprintf(&b, "  %s %6d %9s %9s %9s %9s %10s %4s\n", textutil.Pad(l.Key, kw), l.Steps,
				tok(l.Tokens.Input), tok(l.Tokens.Output), tok(l.Tokens.Reasoning), tok(l.Tokens.CacheRead), cost, errS)
		}
		b.WriteString("\n")
	}
	table("Total", []usage.Line{rep.Total})
	table("By model", rep.ByModel)
	table("By provider", rep.ByProvider)
	table("By agent", rep.ByAgent)
	if rep.Unknown > 0 {
		fmt.Fprintf(&b, "%d steps have no recorded model.\n", rep.Unknown)
	}
	sum := s.sum
	fmt.Fprintf(&b, "\n%s\n", a.s.bold.Render("Session totals stored in session_v2 (for comparison)"))
	fmt.Fprintf(&b, "  input %s · output %s · reasoning %s · cache r/w %s/%s · cost $%.4f\n",
		trace.Count(sum.Tokens.Input), trace.Count(sum.Tokens.Output), trace.Count(sum.Tokens.Reasoning),
		trace.Count(sum.Tokens.CacheRead), trace.Count(sum.Tokens.CacheWrite), sum.Cost)
	return b.String()
}

// ---------- files ----------

type fileEntry struct {
	path  string
	count int
	first int // first visible-row index touching it (-1 if hidden)
	msgID string
	item  int
}

func (s *sessionScreen) fileEntries() []fileEntry {
	idx := map[string]int{}
	var out []fileEntry
	for _, r := range s.rows {
		if r.Kind != trace.KFileChange {
			continue
		}
		for _, f := range r.Files {
			if i, ok := idx[f]; ok {
				out[i].count++
				continue
			}
			idx[f] = len(out)
			out = append(out, fileEntry{path: f, count: 1, msgID: r.MsgID, item: r.Item})
		}
	}
	return out
}

func (s *sessionScreen) filesKey(a *App, k string) tea.Cmd {
	fs := s.fileEntries()
	if s.ov.move(k, len(fs), a.h-8) {
		return nil
	}
	if k == "enter" && len(fs) > 0 {
		f := fs[s.ov.pos]
		s.filter = filterState{}
		s.recompute()
		for vi, ri := range s.visible {
			r := s.rows[ri]
			if r.MsgID == f.msgID && r.Item == f.item && r.Kind == trace.KFileChange {
				s.cur.pos = vi
				break
			}
		}
		s.overlay = ovNone
		return s.detailCmd(a)
	}
	return nil
}

func (s *sessionScreen) filesView(a *App, w, h int) string {
	fs := s.fileEntries()
	head := a.s.dim.Render("Files changed by successful edit/write/apply_patch calls · enter jumps to first change") + "\n"
	if len(fs) == 0 {
		return head + a.s.dim.Render("no file changes recorded in loaded messages")
	}
	dir := s.sum.Directory
	return head + s.ov.window(len(fs), h-1, func(i int, sel bool) string {
		f := fs[i]
		p := f.path
		if rel, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		}
		line := fmt.Sprintf(" %3d× %s", f.count, p)
		if sel {
			return a.s.sel.Render(textutil.Pad(line, w))
		}
		return line
	})
}

// ---------- instructions ----------

type instrState struct {
	loading bool
	srcs    []provenance.Source
	matches [][]provenance.Match
	cancel  context.CancelFunc
	gen     uint64
	open    int // expanded source index, -1 none
	lines   []instrLine
}

type instrLine struct {
	text string
	src  int
}

type instrLoadedMsg struct {
	gen     uint64
	srcs    []provenance.Source
	matches [][]provenance.Match
}

func (s *sessionScreen) instrCmd(a *App) tea.Cmd {
	if s.instr != nil && (s.instr.loading || s.instr.srcs != nil) {
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	gen := a.nextGen()
	s.instr = &instrState{loading: true, cancel: cancel, gen: gen, open: -1}
	st, id := a.st, s.id
	more := s.more
	msgs := s.msgs
	return func() tea.Msg {
		if more {
			// Provenance needs the whole session.
			all, err := trace.LoadAll(ctx, st, id)
			if err != nil {
				return errMsg{gen, err}
			}
			msgs = all
		}
		pv, err := view.AnalyzeProvenance(ctx, st, id, msgs)
		if err != nil {
			return errMsg{gen, err}
		}
		srcs, matches := pv.Sources, pv.Matches
		return instrLoadedMsg{gen, srcs, matches}
	}
}

func (is *instrState) loaded(m instrLoadedMsg) {
	if m.gen != is.gen {
		return
	}
	is.loading, is.srcs, is.matches, is.lines = false, m.srcs, m.matches, nil
	if is.srcs == nil {
		is.srcs = []provenance.Source{}
	}
}

func (is *instrState) key(a *App, s *sessionScreen, k string) tea.Cmd {
	if s.ov.move(k, len(is.lines), a.h-8) {
		return nil
	}
	if (k == "space" || k == "enter" || k == "e") && s.ov.pos < len(is.lines) {
		src := is.lines[s.ov.pos].src
		if src < 0 {
			return nil
		}
		if is.open == src {
			is.open = -1
		} else {
			is.open = src
		}
		is.lines = nil
		is.lines = s.buildInstrLines(a, a.w-4)
		for i, l := range is.lines {
			if l.src == src {
				s.ov.pos = i
				break
			}
		}
	}
	if k == "J" && s.ov.pos < len(is.lines) {
		// Jump to the source's row in the trace.
		src := is.lines[s.ov.pos].src
		if src >= 0 && is.srcs[src].MsgID != "" {
			s.filter = filterState{}
			s.recompute()
			s.jumpTo(is.srcs[src].MsgID)
			s.overlay = ovNone
			return s.detailCmd(a)
		}
	}
	return nil
}

func (s *sessionScreen) instrView(a *App, w, h int) string {
	is := s.instr
	if is == nil || is.loading {
		return a.s.dim.Render("extracting instruction records and correlating (whole session)…")
	}
	if is.lines == nil {
		is.lines = s.buildInstrLines(a, w)
		if s.ov.pos < len(is.lines) && is.lines[s.ov.pos].src < 0 {
			for i, l := range is.lines {
				if l.src >= 0 {
					s.ov.pos = i
					break
				}
			}
		}
	}
	return s.ov.window(len(is.lines), h, func(i int, sel bool) string {
		l := is.lines[i].text
		if sel {
			return a.s.sel.Render(textutil.Pad(textutil.Sanitize(l), w))
		}
		return textutil.Truncate(l, w)
	})
}

func (s *sessionScreen) buildInstrLines(a *App, w int) []instrLine {
	is := s.instr
	var out []instrLine
	add := func(t string, src int) { out = append(out, instrLine{t, src}) }
	add(a.s.dim.Render("Recorded = stored by OpenCode. strong/possible = Himay's textual heuristics. space expands · J jumps to row"), -1)
	add(a.s.warn.Render(provenance.Disclaimer), -1)
	add("", -1)
	if len(is.srcs) == 0 {
		add(fmt.Sprintf("OpenCode stored no instruction records for this session (v%s).", s.sum.Version), -1)
		add(a.s.dim.Render("Older versions did not persist the system prompt, AGENTS.md text, or agent prompts."), -1)
		add(a.s.dim.Render("Only the agent name per step is known; see the trace."), -1)
		return out
	}
	order := make([]int, len(is.srcs))
	for i := range order {
		order[i] = i
	}
	for _, i := range order {
		src := is.srcs[i]
		label := src.Name
		if label == "" {
			label = src.Path
		}
		if label == "" {
			label = src.Key
		}
		if label == "" {
			label = src.Preview
		}
		if src.Removed {
			label += " (removed)"
		}
		ms := is.matches[i]
		var strong, possible int
		for _, m := range ms {
			if m.Evidence == model.Strong {
				strong++
			} else {
				possible++
			}
		}
		corr := a.s.evUnknown.Render("[unknown]")
		if strong > 0 || possible > 0 {
			corr = fmt.Sprintf("%s %d %s %d", a.s.evStrong.Render("strong"), strong, a.s.evPossible.Render("possible"), possible)
		}
		if !view.Correlates(src.Kind) {
			corr = ""
		}
		fold := "▸"
		if is.open == i {
			fold = "▾"
		}
		if corr != "" {
			corr += "  "
		}
		add(fmt.Sprintf("%s %s %-24s %s%s  %s", fold, a.s.evidence(src.Evidence), kindLabel(src.Kind), corr,
			textutil.OneLine(label), a.s.faint.Render(fmt.Sprintf("seq %d · %s", src.Seq, src.Source))), i)
		if is.open != i {
			continue
		}
		if src.Text != "" {
			add(a.s.bold.Render("    content (recorded)"), i)
			lines := textutil.Wrap(src.Text, max(10, w-6))
			for j, l := range lines {
				if j >= 40 {
					add(a.s.faint.Render(fmt.Sprintf("      … %d more lines (open the row's detail for full text)", len(lines)-40)), i)
					break
				}
				add("      "+l, i)
			}
		}
		if len(ms) > 0 {
			add(a.s.bold.Render("    later actions with textual overlap (inferred)"), i)
		}
		for _, m := range ms {
			add(fmt.Sprintf("      %s seq %d %s %s — %s", a.s.evidence(m.Evidence), m.Seq, m.Kind, m.Tool, m.Rule), i)
			add(a.s.faint.Render("          instruction: ")+textutil.OneLine(m.Instr), i)
			add(a.s.faint.Render("          action:      ")+textutil.OneLine(m.Action), i)
		}
	}
	return out
}

func kindLabel(k string) string { return view.KindLabel(k) }

// ---------- help ----------

const helpText = `Himay is a read-only viewer. It never writes to OpenCode's database.

Trace navigation
  j/k ↑/↓        move              g/G        top / bottom (G loads all)
  ctrl+d/ctrl+u  half page         z          collapse/expand all steps
  l / →          on a step: expand, then step into it; elsewhere: open
                 (detail, or the child session on a subagent row)
  h / ←          on a step: collapse; inside a step: jump to it and collapse;
                 on a top-level row or in detail: go back
  enter          open detail; on a subagent row, open the child session
  p              go to parent session        esc/backspace  back

Search & filter
  /              search loaded rows (labels, tools, files, agents, models)
  ctrl+f         deep search: full message content of this session
  n/N            next / previous match
  f              filter by kind, failures, agent, or model

Panels
  i              instruction provenance (recorded facts + labeled correlations)
  u              usage by model / provider / agent
  F              files changed
  e              in detail: expand truncated output
  r              refresh now         L  toggle live refresh (2s)
  q / ctrl+c     quit

Row glyphs
  ▶ user  ◆ step  ¶ text  ∴ reasoning  ⚙ tool  ↳ subagent  ± file change
  ✦ skill  § instructions  ⚒ tools changed  ⇄ switch  ↻ retry  ✗ error
  ⊜ compaction  ◌ idle  · system/synthetic  ? unknown

Evidence labels
  [recorded]  stored by OpenCode
  [strong]    later action quotes the instruction or uses its exact literal
  [possible]  later action appears related
  [unknown]   no reliable connection
  [derived]   computed by Himay from recorded data (e.g. agent switch, retry)

Reasoning shown is only what OpenCode saved. Hidden or encrypted
chain-of-thought is not available and is never reconstructed.`

// ---------- dispatch ----------

func (s *sessionScreen) overlayView(a *App, w, h int) string {
	iw, ih := w-2, h-2
	var title, content string
	switch s.overlay {
	case ovFilter:
		title, content = "Filter", s.filterView(a, iw, ih)
	case ovUsage:
		title = "Usage"
		content = scroll(s.usageView(a, iw), &s.ov, ih)
	case ovFiles:
		title, content = "Files", s.filesView(a, iw, ih)
	case ovInstr:
		title, content = "Instruction provenance", s.instrView(a, iw, ih)
	case ovHelp:
		title = "Help"
		content = scroll(helpText, &s.ov, ih)
	}
	return a.box(title, content, w, h, true)
}

func scroll(text string, c *cursor, h int) string {
	lines := strings.Split(text, "\n")
	c.off = max(0, min(c.pos, len(lines)-h))
	c.pos = c.off
	return strings.Join(lines[c.off:min(len(lines), c.off+h)], "\n")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
