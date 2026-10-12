package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/textutil"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/usage"
)

type overlayKind int

const (
	ovNone overlayKind = iota
	ovDetail
	ovUsage
	ovFiles
	ovInstr
	ovHelp
	ovFilter
)

type sessionScreen struct {
	id      string
	sum     model.SessionSummary
	msgs    []model.Message
	rows    []trace.Row // all rows
	visible []int       // indices into rows after collapse/filter
	cur     cursor
	more    bool
	loading bool
	lastSeq int64
	lastUpd int64

	collapsed map[string]bool // step msgID → collapsed
	filter    filterState
	search    string
	matches   []int // indices into visible
	input     textinput.Model
	prompt    string // "" | "search" | "deep"
	deepHits  map[string]bool

	detail  detailPane
	overlay overlayKind
	ov      cursor
	instr   *instrState

	jumpMsg    string // after load, move the cursor to this message
	searchHint string

	ctx    context.Context
	cancel context.CancelFunc
	gen    uint64
	live   bool
}

func newSessionScreen(id string) *sessionScreen {
	ti := textinput.New()
	ti.Prompt = "/ "
	return &sessionScreen{id: id, collapsed: map[string]bool{}, input: ti, more: true}
}

func (s *sessionScreen) capturing() bool { return s.prompt != "" }
func (s *sessionScreen) close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.instr != nil && s.instr.cancel != nil {
		s.instr.cancel()
	}
	s.detail.cancelLoad()
}

func (s *sessionScreen) hints() string {
	if s.prompt != "" {
		return "enter apply · esc cancel"
	}
	if s.overlay != ovNone {
		return "esc close · j/k scroll · e expand"
	}
	return "j/k move · h/l back/open (fold on steps) · enter detail/child · / search · n/N next · ctrl+f deep · f filter · i instr · u usage · F files · p parent · r refresh · ? help · esc back"
}

type sessionLoadedMsg struct {
	gen  uint64
	sum  model.SessionSummary
	msgs []model.Message
	more bool
}
type pageMsg struct {
	gen  uint64
	msgs []model.Message
	more bool
}
type refreshMsg struct {
	gen     uint64
	added   []model.Message
	changed []model.Message
	sum     model.SessionSummary
}
type deepSessionMsg struct {
	gen  uint64
	hits []store.Hit
}

func (s *sessionScreen) init(a *App) tea.Cmd {
	s.ctx, s.cancel = context.WithCancel(a.ctx)
	s.gen = a.nextGen()
	s.loading = true
	gen, st, ctx, id := s.gen, a.st, s.ctx, s.id
	return func() tea.Msg {
		sum, err := st.Session(ctx, id)
		if err != nil {
			return errMsg{gen, err}
		}
		msgs, more, err := trace.LoadPage(ctx, st, id, -1, trace.PageSize)
		if err != nil {
			return errMsg{gen, err}
		}
		return sessionLoadedMsg{gen, sum, msgs, more}
	}
}

func (s *sessionScreen) loadMore(a *App) tea.Cmd {
	if !s.more || s.loading {
		return nil
	}
	s.loading = true
	gen, st, ctx, id, after := s.gen, a.st, s.ctx, s.id, s.lastSeq
	return func() tea.Msg {
		msgs, more, err := trace.LoadPage(ctx, st, id, after, trace.PageSize)
		if err != nil {
			return errMsg{gen, err}
		}
		return pageMsg{gen, msgs, more}
	}
}

// refresh appends new messages and re-reads messages updated since the last load.
func (s *sessionScreen) refresh(a *App) tea.Cmd {
	if s.loading {
		return nil
	}
	gen, st, ctx, id, after := s.gen, a.st, s.ctx, s.id, s.lastSeq
	var stale []string
	for i := len(s.msgs) - 1; i >= 0 && i >= len(s.msgs)-3; i-- {
		stale = append(stale, s.msgs[i].ID) // tail rows may still be streaming
	}
	return func() tea.Msg {
		sum, err := st.Session(ctx, id)
		if err != nil {
			return errMsg{gen, err}
		}
		var added []model.Message
		for {
			page, more, err := trace.LoadPage(ctx, st, id, after, trace.PageSize)
			if err != nil {
				return errMsg{gen, err}
			}
			added = append(added, page...)
			if !more || len(page) == 0 {
				break
			}
			after = page[len(page)-1].Seq
		}
		changed, err := trace.Reload(ctx, st, id, stale)
		if err != nil {
			return errMsg{gen, err}
		}
		return refreshMsg{gen, added, changed, sum}
	}
}

func (s *sessionScreen) absorb(msgs []model.Message) {
	s.msgs = append(s.msgs, msgs...)
	if n := len(s.msgs); n > 0 {
		s.lastSeq = s.msgs[n-1].Seq
	}
	s.rebuild()
}

// rebuild recomputes rows and the visible index, keeping the cursor on the same row.
func (s *sessionScreen) rebuild() {
	var keepMsg string
	keepItem := -2
	var keepKind trace.Kind
	if r := s.selected(); r != nil {
		keepMsg, keepItem, keepKind = r.MsgID, r.Item, r.Kind
	}
	s.rows = trace.Build(s.msgs)
	s.recompute()
	if keepMsg != "" {
		for vi, ri := range s.visible {
			r := s.rows[ri]
			if r.MsgID == keepMsg && r.Item == keepItem && r.Kind == keepKind {
				s.cur.pos = vi
				break
			}
		}
	}
}

func (s *sessionScreen) recompute() {
	s.visible = s.visible[:0]
	filtering := s.filter.active()
	skipDepth := -1
	for i, r := range s.rows {
		if skipDepth >= 0 && r.Depth > skipDepth {
			continue
		}
		skipDepth = -1
		if filtering {
			if s.filter.match(r) {
				s.visible = append(s.visible, i)
			}
			continue
		}
		s.visible = append(s.visible, i)
		if r.Kind == trace.KStep && s.collapsed[r.MsgID] {
			skipDepth = r.Depth
		}
	}
	s.recomputeMatches()
}

func (s *sessionScreen) recomputeMatches() {
	s.matches = s.matches[:0]
	if s.search == "" && s.deepHits == nil {
		return
	}
	q := strings.ToLower(s.search)
	for vi, ri := range s.visible {
		r := s.rows[ri]
		if s.deepHits != nil {
			if s.deepHits[r.MsgID] && (r.Item < 0 || r.Kind != trace.KStep) {
				s.matches = append(s.matches, vi)
			}
			continue
		}
		if strings.Contains(r.Searchable(), q) {
			s.matches = append(s.matches, vi)
		}
	}
}

func (s *sessionScreen) selected() *trace.Row {
	if s.cur.pos < 0 || s.cur.pos >= len(s.visible) {
		return nil
	}
	return &s.rows[s.visible[s.cur.pos]]
}

func (s *sessionScreen) update(a *App, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case sessionLoadedMsg:
		if msg.gen != s.gen {
			return nil
		}
		s.loading, s.sum, s.more = false, msg.sum, msg.more
		s.absorb(msg.msgs)
		_, s.lastUpd = s.sum.Created.UnixMilli(), s.sum.Updated.UnixMilli()
		if s.jumpMsg != "" {
			s.jumpTo(s.jumpMsg)
		}
		return nil
	case pageMsg:
		if msg.gen != s.gen {
			return nil
		}
		s.loading, s.more = false, msg.more
		s.absorb(msg.msgs)
		if s.jumpMsg != "" {
			s.jumpTo(s.jumpMsg)
		}
		return nil
	case refreshMsg:
		if msg.gen != s.gen {
			return nil
		}
		s.sum = msg.sum
		byID := map[string]model.Message{}
		for _, m := range msg.changed {
			byID[m.ID] = m
		}
		for i, m := range s.msgs {
			if c, ok := byID[m.ID]; ok {
				s.msgs[i] = c
			}
		}
		s.absorb(msg.added)
		a.status = fmt.Sprintf("refreshed · +%d messages", len(msg.added))
		return nil
	case liveTickMsg:
		if !s.live || msg.gen != s.gen {
			return nil
		}
		return tea.Batch(s.refresh(a), liveTick(s.gen))
	case deepSessionMsg:
		if msg.gen != s.gen {
			return nil
		}
		s.deepHits = map[string]bool{}
		for _, h := range msg.hits {
			s.deepHits[h.MsgID] = true
		}
		s.recomputeMatches()
		a.status = fmt.Sprintf("deep: %d messages match %q · n/N to jump", len(msg.hits), s.search)
		s.nextMatch(1, true)
		return nil
	case detailLoadedMsg:
		s.detail.loaded(msg)
		return nil
	case instrLoadedMsg:
		if s.instr != nil {
			s.instr.loaded(msg)
		}
		return nil
	case tea.KeyPressMsg:
		return s.key(a, msg)
	}
	if s.prompt != "" {
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return cmd
	}
	return nil
}

type liveTickMsg struct{ gen uint64 }

func liveTick(gen uint64) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return liveTickMsg{gen} })
}

func (s *sessionScreen) jumpTo(msgID string) {
	for vi, ri := range s.visible {
		if s.rows[ri].MsgID == msgID {
			s.cur.pos = vi
			s.jumpMsg = ""
			return
		}
	}
}

func (s *sessionScreen) nextMatch(dir int, inclusive bool) {
	if len(s.matches) == 0 {
		return
	}
	if dir > 0 {
		for _, m := range s.matches {
			if m > s.cur.pos || (inclusive && m == s.cur.pos) {
				s.cur.pos = m
				return
			}
		}
		s.cur.pos = s.matches[0]
		return
	}
	for i := len(s.matches) - 1; i >= 0; i-- {
		if s.matches[i] < s.cur.pos {
			s.cur.pos = s.matches[i]
			return
		}
	}
	s.cur.pos = s.matches[len(s.matches)-1]
}

func (s *sessionScreen) traceHeight(a *App) int { return a.h - 2 - 3 - 2 }

func (s *sessionScreen) key(a *App, msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if s.prompt != "" {
		return s.promptKey(a, msg)
	}
	if s.overlay != ovNone {
		return s.overlayKey(a, k)
	}
	h := s.traceHeight(a)
	if s.cur.move(k, len(s.visible), h) {
		if s.cur.pos >= len(s.visible)-10 {
			return tea.Batch(s.loadMore(a), s.detailCmd(a))
		}
		return s.detailCmd(a)
	}
	switch k {
	case "esc", "backspace":
		if s.search != "" || s.deepHits != nil {
			s.search, s.deepHits = "", nil
			s.recomputeMatches()
			a.status = ""
			return nil
		}
		if s.filter.active() {
			s.filter = filterState{}
			s.recompute()
			a.status = ""
			return nil
		}
		a.pop()
		a.status = ""
		return nil
	case "h", "left":
		return s.left(a)
	case "l", "right":
		return s.right(a)
	case "z":
		// Toggle collapsing every step.
		allCollapsed := len(s.collapsed) > 0
		s.collapsed = map[string]bool{}
		if !allCollapsed {
			for _, r := range s.rows {
				if r.Kind == trace.KStep && r.HasKids {
					s.collapsed[r.MsgID] = true
				}
			}
		}
		s.rebuild()
		return nil
	case "enter":
		return s.open(a)
	case "p":
		if s.sum.ParentID != "" {
			parent := s.sum.ParentID
			// If the parent is the screen below, just go back.
			if len(a.stack) >= 2 {
				if ps, ok := a.stack[len(a.stack)-2].(*sessionScreen); ok && ps.id == parent {
					a.pop()
					return nil
				}
			}
			ns := newSessionScreen(parent)
			// Land on the tool call that spawned this session.
			ns.jumpMsg = ""
			return a.push(ns)
		}
		a.status = "this session has no parent"
		return nil
	case "/":
		return s.openPrompt("search", "/ ", "filter-free search in this session (labels, tools, files)")
	case "ctrl+f":
		return s.openPrompt("deep", "deep› ", "search full message content in this session")
	case "n":
		s.nextMatch(1, false)
		return s.detailCmd(a)
	case "N":
		s.nextMatch(-1, false)
		return s.detailCmd(a)
	case "f":
		s.overlay, s.ov = ovFilter, cursor{}
		return nil
	case "u":
		s.overlay, s.ov = ovUsage, cursor{}
		if s.more {
			a.status = "usage covers loaded messages only — press G to load all"
		}
		return nil
	case "F":
		s.overlay, s.ov = ovFiles, cursor{}
		return nil
	case "i":
		s.overlay, s.ov = ovInstr, cursor{}
		return s.instrCmd(a)
	case "?":
		s.overlay, s.ov = ovHelp, cursor{}
		return nil
	case "r":
		return s.refresh(a)
	case "L":
		s.live = !s.live
		if s.live {
			a.status = "live refresh on (2s)"
			return liveTick(s.gen)
		}
		a.status = "live refresh off"
		return nil
	case "G":
		// Load the full session so G truly reaches the end.
		return s.loadAllCmd(a)
	}
	return nil
}

// open shows a row's detail, or descends into the child session of a subagent row.
func (s *sessionScreen) open(a *App) tea.Cmd {
	r := s.selected()
	if r == nil {
		return nil
	}
	if r.Kind == trace.KSubagent && r.Child != "" {
		return a.push(newSessionScreen(r.Child))
	}
	s.overlay, s.ov = ovDetail, cursor{}
	return s.detailCmd(a)
}

// right (l/→) depends on the row: a folded step expands, an open step steps
// into its first child, and any other row opens.
func (s *sessionScreen) right(a *App) tea.Cmd {
	r := s.selected()
	if r == nil {
		return nil
	}
	if r.Kind == trace.KStep && r.HasKids {
		if s.collapsed[r.MsgID] {
			delete(s.collapsed, r.MsgID)
			s.recompute()
			return nil
		}
		s.cur.move("j", len(s.visible), s.traceHeight(a))
		return s.detailCmd(a)
	}
	return s.open(a)
}

// left (h/←) depends on the row: an open step folds, a row inside a step
// folds its step and moves to it, and a top-level row goes back. A folded
// step stays put so a double press never leaves the session by accident.
func (s *sessionScreen) left(a *App) tea.Cmd {
	r := s.selected()
	if r == nil {
		return nil
	}
	if r.Kind == trace.KStep {
		if r.HasKids && !s.collapsed[r.MsgID] {
			s.collapsed[r.MsgID] = true
			s.recompute()
		}
		return nil
	}
	if r.Depth > 0 {
		msgID := r.MsgID
		s.collapsed[msgID] = true
		s.recompute()
		for vi, ri := range s.visible {
			if s.rows[ri].MsgID == msgID && s.rows[ri].Kind == trace.KStep {
				s.cur.pos = vi
			}
		}
		return s.detailCmd(a)
	}
	a.pop()
	a.status = ""
	return nil
}

func (s *sessionScreen) loadAllCmd(a *App) tea.Cmd {
	if !s.more {
		return nil
	}
	s.loading = true
	gen, st, ctx, id, after := s.gen, a.st, s.ctx, s.id, s.lastSeq
	return func() tea.Msg {
		var all []model.Message
		for {
			page, more, err := trace.LoadPage(ctx, st, id, after, trace.PageSize)
			if err != nil {
				return errMsg{gen, err}
			}
			all = append(all, page...)
			if !more || len(page) == 0 {
				return pageMsg{gen, all, false}
			}
			after = page[len(page)-1].Seq
		}
	}
}

func (s *sessionScreen) openPrompt(kind, prompt, placeholder string) tea.Cmd {
	s.prompt = kind
	s.input.Reset()
	s.input.Prompt = prompt
	s.input.Placeholder = placeholder
	return s.input.Focus()
}

func (s *sessionScreen) promptKey(a *App, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		s.prompt = ""
		s.input.Blur()
		return nil
	case "enter":
		kind := s.prompt
		q := strings.TrimSpace(s.input.Value())
		s.prompt = ""
		s.input.Blur()
		s.search, s.deepHits = q, nil
		if q == "" {
			s.recomputeMatches()
			return nil
		}
		if kind == "deep" {
			a.status = "searching message content…"
			gen, st, ctx, id := s.gen, a.st, s.ctx, s.id
			return tea.Batch(s.loadAllCmd(a), func() tea.Msg {
				hits, err := st.SearchSession(ctx, id, q, 2000)
				if err != nil {
					return errMsg{gen, err}
				}
				return deepSessionMsg{gen, hits}
			})
		}
		s.recomputeMatches()
		s.nextMatch(1, true)
		a.status = fmt.Sprintf("%d matches for %q (loaded rows) · n/N", len(s.matches), q)
		return s.detailCmd(a)
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

func (s *sessionScreen) overlayKey(a *App, k string) tea.Cmd {
	if k == "esc" || k == "backspace" || (s.overlay == ovDetail && (k == "enter" || k == "h" || k == "left")) {
		s.overlay = ovNone
		return nil
	}
	switch s.overlay {
	case ovFilter:
		return s.filterKey(a, k)
	case ovDetail:
		if k == "e" {
			return s.detail.expand(a)
		}
		s.ov.move(k, s.detail.lineCount(), a.h-6)
	case ovInstr:
		if s.instr != nil {
			return s.instr.key(a, s, k)
		}
	case ovFiles:
		return s.filesKey(a, k)
	default:
		s.ov.move(k, 10000, a.h-6)
	}
	return nil
}

// ---------- rendering ----------

func (s *sessionScreen) view(a *App, w, h int) string {
	summary := s.summaryView(a, w)
	sh := strings.Count(summary, "\n") + 1
	bodyH := h - sh
	if s.prompt != "" {
		bodyH--
	}
	var body string
	switch {
	case s.overlay != ovNone && s.overlay != ovDetail:
		body = s.overlayView(a, w, bodyH)
	case s.overlay == ovDetail || w < 120:
		if s.overlay == ovDetail {
			body = a.box("Detail", s.detail.view(a, w-2, bodyH-2, s.ov.off), w, bodyH, true)
		} else {
			body = a.box(s.traceTitle(), s.traceView(a, w-2, bodyH-2), w, bodyH, true)
		}
	default:
		tw := w * 58 / 100
		left := a.box(s.traceTitle(), s.traceView(a, tw-2, bodyH-2), tw, bodyH, true)
		right := a.box("Detail", s.detail.view(a, w-tw-2, bodyH-2, 0), w-tw, bodyH, false)
		body = joinH(left, right)
	}
	out := summary + "\n" + body
	if s.prompt != "" {
		s.input.SetWidth(w - 4)
		out += "\n" + s.input.View()
	}
	return out
}

func (s *sessionScreen) traceTitle() string {
	t := fmt.Sprintf("Trace · %d rows", len(s.visible))
	if s.more {
		t += "+"
	}
	if s.filter.active() {
		t += " · filter: " + s.filter.describe()
	}
	if s.search != "" {
		t += fmt.Sprintf(" · %q %d", s.search, len(s.matches))
	}
	if s.live {
		t += " · live"
	}
	return t
}

func (s *sessionScreen) summaryView(a *App, w int) string {
	sum := s.sum
	if sum.ID == "" {
		return a.s.dim.Render("loading " + s.id + "…")
	}
	title := sum.Title
	if title == "" {
		title = sum.ID
	}
	l1 := a.s.bold.Render(textutil.Truncate(title, w/2)) + a.s.dim.Render("  "+sum.Directory)
	rep := usage.Build(s.msgs)
	errors, tools, files := 0, 0, map[string]bool{}
	for _, r := range s.rows {
		switch r.Kind {
		case trace.KError:
			errors++
		case trace.KTool, trace.KSubagent, trace.KSkill:
			if r.Item >= 0 {
				tools++
			}
			if r.Status == trace.SFail {
				errors++
			}
		case trace.KFileChange:
			for _, f := range r.Files {
				files[f] = true
			}
		}
	}
	parts := []string{
		"default " + orDash(sum.Agent) + "·" + sum.Model.ID,
		fmt.Sprintf("models %d", len(rep.ByModel)),
		fmt.Sprintf("agents %d", len(rep.ByAgent)),
		fmt.Sprintf("%s tok", trace.Count(rep.Total.Tokens.Total())),
		fmt.Sprintf("$%.2f reported", rep.Total.Cost),
		fmt.Sprintf("%d tools", tools),
	}
	errS := fmt.Sprintf("%d errors", errors)
	if errors > 0 {
		errS = a.s.fail.Render(errS)
	}
	parts = append(parts, errS, fmt.Sprintf("%d files", len(files)), "v"+sum.Version)
	if sum.ParentID != "" {
		parts = append(parts, "subagent of "+sum.ParentID)
	}
	if sum.Children > 0 {
		parts = append(parts, fmt.Sprintf("↳%d subagents", sum.Children))
	}
	if s.more {
		parts = append(parts, a.s.warn.Render("partial (more pages)"))
	}
	l2 := a.s.dim.Render(strings.Join(parts, " · "))
	return textutil.Truncate(l1, w) + "\n" + textutil.Truncate(l2, w)
}

func (s *sessionScreen) traceView(a *App, w, h int) string {
	if len(s.visible) == 0 {
		if s.loading {
			return a.s.dim.Render("loading trace…")
		}
		if s.filter.active() {
			return a.s.dim.Render("no rows match the filter (esc clears)")
		}
		return a.s.dim.Render("no messages recorded in session_message for this session")
	}
	start := s.sum.Created
	if len(s.msgs) > 0 && !s.msgs[0].Created.IsZero() {
		start = s.msgs[0].Created
	}
	matchSet := map[int]bool{}
	for _, m := range s.matches {
		matchSet[m] = true
	}
	out := s.cur.window(len(s.visible), h, func(vi int, sel bool) string {
		r := s.rows[s.visible[vi]]
		return s.rowLine(a, r, start, w, sel, matchSet[vi])
	})
	if s.more && s.cur.off+h >= len(s.visible) {
		out += "\n" + a.s.faint.Render("  … more (scroll or G to load)")
	}
	return out
}

func (s *sessionScreen) rowLine(a *App, r trace.Row, start time.Time, w int, sel, match bool) string {
	ts := a.s.faint.Render(textutil.Pad(trace.Elapsed(r.Time, start), 8))
	indent := strings.Repeat("  ", r.Depth)
	fold := " "
	if r.Kind == trace.KStep && r.HasKids {
		if s.collapsed[r.MsgID] {
			fold = "▸"
		} else {
			fold = "▾"
		}
	}
	ks := a.s.kind(r.Kind)
	glyph := ks.Render(trace.Glyph(r.Kind))
	var label string
	switch r.Kind {
	case trace.KStep:
		label = ks.Render(orDash(r.Agent)) + a.s.dim.Render(" · ") + a.s.info.Render(r.Model.String()) + "  " + a.s.dim.Render(r.Label)
	case trace.KUser:
		label = ks.Render("USER ") + textutil.OneLine(r.Label)
	default:
		label = ks.Render(textutil.OneLine(r.Label))
	}
	suffix := ""
	if d := trace.Duration(r.Dur); d != "" {
		suffix += " " + a.s.faint.Render(d)
	}
	if r.Child != "" {
		suffix += " " + a.s.accent.Render("↳open")
	}
	if r.Evidence == model.Derived {
		suffix += " " + a.s.evidence(r.Evidence)
	}
	mark := " "
	if match {
		mark = a.s.warn.Render("●")
	}
	left := mark + ts + indent + fold + glyph + " " + a.s.status(r.Status) + " "
	avail := w - textutil.Width(left) - textutil.Width(suffix)
	line := left + textutil.Truncate(label, max(4, avail)) + suffix
	if sel {
		return a.s.sel.Render(textutil.Pad(textutil.Truncate(textutil.Sanitize(line), w), w))
	}
	return textutil.Truncate(line, w)
}

var _ = time.Second
