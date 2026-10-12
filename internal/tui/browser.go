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
)

const sessionPage = 100

// sessNode is a row in the session tree (root sessions with lazily loaded children).
type sessNode struct {
	s        model.SessionSummary
	depth    int
	expanded bool
	loading  bool
}

type browser struct {
	projects []model.Project
	pc       cursor
	nodes    []sessNode
	sc       cursor
	focus    int // 0 projects, 1 sessions
	more     bool
	archived bool

	// search
	input     textinput.Model
	searching bool // prompt open
	deep      bool // prompt is for deep search
	results   []searchResult
	resultsOn bool // showing search results instead of a project
	query     string
	progress  string // deep search progress
	searchCtx context.Context
	cancel    context.CancelFunc
	gen       uint64
}

type searchResult struct {
	sessionID, title, snippet, msgID string
	seq                              int64
}

func newBrowser() *browser {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search session titles"
	return &browser{input: ti}
}

func (b *browser) capturing() bool { return b.searching }
func (b *browser) close() {
	if b.cancel != nil {
		b.cancel()
	}
}

func (b *browser) hints() string {
	if b.searching {
		return "enter search · esc cancel"
	}
	if b.progress != "" {
		return "esc stop search"
	}
	return "tab focus · enter open · →/← expand subagents · / titles · ctrl+f deep search · a archived · q quit"
}

type projectsMsg struct {
	gen  uint64
	list []model.Project
}
type sessionsMsg struct {
	gen     uint64
	project string
	list    []model.SessionSummary
	append  bool
}
type childrenMsg struct {
	parent string
	list   []model.SessionSummary
}
type titleResultsMsg struct {
	gen  uint64
	list []model.SessionSummary
}
type deepChunkMsg struct {
	gen    uint64
	hits   []store.Hit
	after  int64
	maxRow int64
	done   bool
}

func (b *browser) init(a *App) tea.Cmd {
	gen := a.nextGen()
	b.gen = gen
	st, ctx := a.st, a.ctx
	return func() tea.Msg {
		ps, err := st.Projects(ctx)
		if err != nil {
			return errMsg{gen, err}
		}
		return projectsMsg{gen, ps}
	}
}

func (b *browser) selectedProject() *model.Project {
	if len(b.projects) == 0 {
		return nil
	}
	return &b.projects[b.pc.pos]
}

func (b *browser) loadSessions(a *App, appendPage bool) tea.Cmd {
	p := b.selectedProject()
	if p == nil {
		return nil
	}
	gen := a.nextGen()
	b.gen = gen
	var after store.Cursor
	if appendPage && len(b.nodes) > 0 {
		// last root node
		for i := len(b.nodes) - 1; i >= 0; i-- {
			if b.nodes[i].depth == 0 {
				after = store.Cursor{Updated: b.nodes[i].s.Updated.UnixMilli(), ID: b.nodes[i].s.ID}
				break
			}
		}
	}
	st, ctx, pid, arch := a.st, a.ctx, p.ID, b.archived
	return func() tea.Msg {
		list, err := st.RootSessions(ctx, pid, after, sessionPage, arch)
		if err != nil {
			return errMsg{gen, err}
		}
		return sessionsMsg{gen, pid, list, appendPage}
	}
}

func (b *browser) update(a *App, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case projectsMsg:
		if msg.gen != b.gen {
			return nil
		}
		b.projects = msg.list
		return b.loadSessions(a, false)
	case sessionsMsg:
		if msg.gen != b.gen {
			return nil
		}
		nodes := make([]sessNode, 0, len(msg.list))
		for _, s := range msg.list {
			nodes = append(nodes, sessNode{s: s})
		}
		if msg.append {
			b.nodes = append(b.nodes, nodes...)
		} else {
			b.nodes, b.sc = nodes, cursor{}
		}
		b.more = len(msg.list) == sessionPage
		return nil
	case childrenMsg:
		for i := range b.nodes {
			if b.nodes[i].s.ID == msg.parent && b.nodes[i].loading {
				b.nodes[i].loading, b.nodes[i].expanded = false, true
				kids := make([]sessNode, len(msg.list))
				for j, s := range msg.list {
					kids[j] = sessNode{s: s, depth: b.nodes[i].depth + 1}
				}
				b.nodes = append(b.nodes[:i+1], append(kids, b.nodes[i+1:]...)...)
				break
			}
		}
		return nil
	case titleResultsMsg:
		if msg.gen != b.gen {
			return nil
		}
		b.results = b.results[:0]
		for _, s := range msg.list {
			b.results = append(b.results, searchResult{sessionID: s.ID, title: s.Title,
				snippet: s.Agent + " · " + s.Model.String() + " · " + age(s.Updated)})
		}
		b.sc = cursor{}
		return nil
	case deepChunkMsg:
		if msg.gen != b.gen {
			return nil
		}
		for _, h := range msg.hits {
			if len(b.results) >= 500 {
				break
			}
			b.results = append(b.results, searchResult{sessionID: h.SessionID, msgID: h.MsgID, seq: h.Seq,
				title: h.Type + " seq " + fmt.Sprint(h.Seq), snippet: textutil.OneLine(h.Snippet)})
		}
		if msg.done || len(b.results) >= 500 {
			b.progress = ""
			a.status = fmt.Sprintf("deep search: %d matches", len(b.results))
			return nil
		}
		pct := 0
		if msg.maxRow > 0 {
			pct = int(100 * msg.after / msg.maxRow)
		}
		b.progress = fmt.Sprintf("searching… %d%% · %d matches", pct, len(b.results))
		a.status = b.progress
		return b.deepChunk(a, msg.gen, msg.after, msg.maxRow)
	case tea.KeyPressMsg:
		return b.key(a, msg)
	}
	if b.searching {
		var cmd tea.Cmd
		b.input, cmd = b.input.Update(msg)
		return cmd
	}
	return nil
}

func (b *browser) key(a *App, msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if b.searching {
		switch k {
		case "esc":
			b.searching = false
			b.input.Blur()
			return nil
		case "enter":
			b.searching = false
			b.input.Blur()
			return b.runSearch(a, strings.TrimSpace(b.input.Value()))
		}
		var cmd tea.Cmd
		b.input, cmd = b.input.Update(msg)
		return cmd
	}
	if k == "esc" {
		if b.progress != "" {
			b.stopSearch(a)
			a.status = fmt.Sprintf("deep search stopped: %d matches", len(b.results))
			return nil
		}
		if b.resultsOn {
			b.resultsOn, b.results, b.query = false, nil, ""
			a.status = ""
			return b.loadSessions(a, false)
		}
		return nil
	}
	h := a.h - 4
	switch k {
	case "tab", "shift+tab":
		b.focus = 1 - b.focus
		return nil
	case "/":
		return b.openPrompt(false)
	case "ctrl+f":
		return b.openPrompt(true)
	case "a":
		b.archived = !b.archived
		a.status = map[bool]string{true: "showing archived", false: "hiding archived"}[b.archived]
		return b.loadSessions(a, false)
	}
	if b.focus == 0 && !b.resultsOn {
		if k == "enter" || k == "l" || k == "right" {
			b.focus = 1
			return nil
		}
		old := b.pc.pos
		if b.pc.move(k, len(b.projects), h) && old != b.pc.pos {
			return b.loadSessions(a, false)
		}
		return nil
	}
	if b.resultsOn {
		if b.sc.move(k, len(b.results), h) {
			return nil
		}
		if k == "enter" && len(b.results) > 0 {
			r := b.results[b.sc.pos]
			ss := newSessionScreen(r.sessionID)
			ss.jumpMsg, ss.searchHint = r.msgID, b.query
			return a.push(ss)
		}
		return nil
	}
	if b.sc.move(k, len(b.nodes), h) {
		if b.more && b.sc.pos >= len(b.nodes)-5 {
			b.more = false
			return b.loadSessions(a, true)
		}
		return nil
	}
	if len(b.nodes) == 0 {
		return nil
	}
	n := &b.nodes[b.sc.pos]
	switch k {
	case "enter":
		return a.push(newSessionScreen(n.s.ID))
	case "l", "right", "space":
		if n.s.Children > 0 && !n.expanded && !n.loading {
			n.loading = true
			id, st, ctx := n.s.ID, a.st, a.ctx
			return func() tea.Msg {
				kids, err := st.Children(ctx, id)
				if err != nil {
					return errMsg{0, err}
				}
				return childrenMsg{id, kids}
			}
		}
		if k == "space" && n.expanded {
			b.collapse(b.sc.pos)
		}
	case "h", "left":
		if n.expanded {
			b.collapse(b.sc.pos)
		} else if n.depth > 0 {
			for i := b.sc.pos - 1; i >= 0; i-- {
				if b.nodes[i].depth < n.depth {
					b.sc.pos = i
					break
				}
			}
		} else {
			b.focus = 0
		}
	}
	return nil
}

func (b *browser) collapse(i int) {
	d := b.nodes[i].depth
	j := i + 1
	for j < len(b.nodes) && b.nodes[j].depth > d {
		j++
	}
	b.nodes = append(b.nodes[:i+1], b.nodes[j:]...)
	b.nodes[i].expanded = false
}

func (b *browser) openPrompt(deep bool) tea.Cmd {
	b.searching, b.deep = true, deep
	b.input.Reset()
	if deep {
		b.input.Prompt = "deep› "
		b.input.Placeholder = "search all message content (scans the whole DB; esc stops)"
	} else {
		b.input.Prompt = "/ "
		b.input.Placeholder = "search session titles"
	}
	return b.input.Focus()
}

func (b *browser) stopSearch(a *App) {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.progress = ""
	b.gen = a.nextGen() // drop any in-flight chunk result
}

func (b *browser) runSearch(a *App, q string) tea.Cmd {
	if q == "" {
		return nil
	}
	b.stopSearch(a)
	b.query, b.resultsOn, b.results, b.focus = q, true, nil, 1
	gen := a.nextGen()
	b.gen = gen
	ctx, cancel := context.WithCancel(a.ctx)
	b.searchCtx, b.cancel = ctx, cancel
	st := a.st
	if !b.deep {
		a.status = "title search: " + q
		return func() tea.Msg {
			list, err := st.SearchTitles(ctx, q, 200)
			if err != nil {
				return errMsg{gen, err}
			}
			return titleResultsMsg{gen, list}
		}
	}
	b.progress = "searching… 0%"
	a.status = b.progress
	return func() tea.Msg {
		mx, err := st.MaxRowID(ctx)
		if err != nil {
			return errMsg{gen, err}
		}
		return deepChunkMsg{gen: gen, after: 0, maxRow: mx}
	}
}

func (b *browser) deepChunk(a *App, gen uint64, after, maxRow int64) tea.Cmd {
	st, ctx, q := a.st, b.searchCtx, b.query
	if ctx == nil {
		return nil
	}
	return func() tea.Msg {
		hits, hi, err := st.DeepSearchChunk(ctx, q, after, 400)
		if err != nil {
			return errMsg{gen, err}
		}
		return deepChunkMsg{gen: gen, hits: hits, after: hi, maxRow: maxRow, done: hi == 0}
	}
}

func (b *browser) view(a *App, w, h int) string {
	if b.searching {
		h--
	}
	var body string
	if w < 70 {
		// Narrow: one pane at a time.
		if b.focus == 0 && !b.resultsOn {
			body = a.box("Projects", b.projectList(a, w-2, h-2), w, h, true)
		} else {
			body = a.box(b.rightTitle(), b.rightList(a, w-2, h-2), w, h, true)
		}
	} else {
		lw := min(36, w/3)
		left := a.box("Projects", b.projectList(a, lw-2, h-2), lw, h, b.focus == 0 && !b.resultsOn)
		right := a.box(b.rightTitle(), b.rightList(a, w-lw-2, h-2), w-lw, h, b.focus == 1 || b.resultsOn)
		body = joinH(left, right)
	}
	if b.searching {
		b.input.SetWidth(w - 4)
		body += "\n" + b.input.View()
	}
	return body
}

func (b *browser) rightTitle() string {
	if b.resultsOn {
		kind := "Title"
		if b.deep {
			kind = "Deep"
		}
		return fmt.Sprintf("%s search %q · %d", kind, b.query, len(b.results))
	}
	if p := b.selectedProject(); p != nil {
		return "Sessions · " + projectName(*p)
	}
	return "Sessions"
}

func (b *browser) projectList(a *App, w, h int) string {
	if len(b.projects) == 0 {
		return a.s.dim.Render("loading projects…")
	}
	return b.pc.window(len(b.projects), h, func(i int, sel bool) string {
		p := b.projects[i]
		cnt := fmt.Sprintf("%4d", p.Sessions)
		line := textutil.Pad(" "+projectName(p), w-5) + a.s.dim.Render(cnt)
		if sel {
			if b.focus == 0 && !b.resultsOn {
				return a.s.sel.Render(textutil.Pad(line, w))
			}
			return a.s.selDim.Render(textutil.Pad(line, w))
		}
		return line
	})
}

func (b *browser) rightList(a *App, w, h int) string {
	if b.resultsOn {
		if len(b.results) == 0 {
			if b.progress != "" {
				return a.s.dim.Render(b.progress)
			}
			return a.s.dim.Render("no matches")
		}
		return b.sc.window(len(b.results), h, func(i int, sel bool) string {
			r := b.results[i]
			line := " " + textutil.Pad(r.title, min(40, w/2)) + " " + a.s.dim.Render(r.snippet)
			if sel {
				return a.s.sel.Render(textutil.Pad(textutil.Truncate(line, w), w))
			}
			return textutil.Truncate(line, w)
		})
	}
	if len(b.nodes) == 0 {
		return a.s.dim.Render("no sessions")
	}
	return b.sc.window(len(b.nodes), h, func(i int, sel bool) string {
		n := b.nodes[i]
		marker := "  "
		switch {
		case n.loading:
			marker = "… "
		case n.s.Children > 0 && n.expanded:
			marker = "▾ "
		case n.s.Children > 0:
			marker = "▸ "
		}
		indent := strings.Repeat("  ", n.depth)
		if n.depth > 0 {
			indent = strings.Repeat("  ", n.depth-1) + "└ "
		}
		title := n.s.Title
		if title == "" {
			title = n.s.ID
		}
		meta := age(n.s.Updated)
		if n.s.Children > 0 {
			meta = fmt.Sprintf("↳%d %s", n.s.Children, meta)
		}
		if n.s.Archived != nil {
			meta += " archived"
		}
		// Agent and model are shown only when there is room; the title wins.
		if w >= 90 {
			meta = fmt.Sprintf("%-12s %-22s %s", textutil.Truncate(orDash(n.s.Agent), 12),
				textutil.Truncate(n.s.Model.ID, 22), meta)
		} else if w >= 64 {
			meta = fmt.Sprintf("%-12s %s", textutil.Truncate(orDash(n.s.Agent), 12), meta)
		}
		prefix := " " + indent + marker
		tw := max(8, w-textutil.Width(prefix)-textutil.Width(meta)-1)
		line := prefix + textutil.Pad(title, tw) + " " + a.s.dim.Render(meta)
		if sel {
			if b.focus == 1 {
				return a.s.sel.Render(textutil.Pad(textutil.Truncate(line, w), w))
			}
			return a.s.selDim.Render(textutil.Pad(textutil.Truncate(line, w), w))
		}
		return textutil.Truncate(line, w)
	})
}

func projectName(p model.Project) string {
	if p.Name != "" {
		return p.Name
	}
	if p.Worktree == "/" {
		return "(global)"
	}
	parts := strings.Split(strings.TrimRight(p.Worktree, "/"), "/")
	return parts[len(parts)-1]
}

func age(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("2006-01")
}

// joinH places two equal-height blocks side by side.
func joinH(l, r string) string {
	ll, rl := strings.Split(l, "\n"), strings.Split(r, "\n")
	n := max(len(ll), len(rl))
	lw := 0
	for _, s := range ll {
		lw = max(lw, textutil.Width(s))
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		ls, rs := "", ""
		if i < len(ll) {
			ls = ll[i]
		}
		if i < len(rl) {
			rs = rl[i]
		}
		b.WriteString(textutil.Pad(ls, lw) + rs)
	}
	return b.String()
}
