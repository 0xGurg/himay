package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/textutil"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/view"
)

// detailPane shows the selected row's full content, loaded lazily.
type detailPane struct {
	key      string // msgID#item of the loaded content
	row      trace.Row
	msg      *model.Message
	sections []section
	expanded bool
	loading  bool
	cancel   context.CancelFunc
	gen      uint64
	lines    []string // rendered cache
	lineW    int
}

type section struct {
	title string
	body  string
	diff  bool
}

type detailLoadedMsg struct {
	gen      uint64
	key      string
	sections []section
}

func (d *detailPane) cancelLoad() {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

func (d *detailPane) lineCount() int { return len(d.lines) }

func (d *detailPane) loaded(m detailLoadedMsg) {
	if m.gen != d.gen || m.key != d.key {
		return
	}
	d.loading = false
	d.sections = m.sections
	d.lines = nil
}

func (s *sessionScreen) findMsg(id string) *model.Message { return view.FindMsg(s.msgs, id) }

// detailCmd starts loading detail for the selected row if it changed.
func (s *sessionScreen) detailCmd(a *App) tea.Cmd {
	r := s.selected()
	if r == nil {
		return nil
	}
	key := fmt.Sprintf("%s#%d#%d", r.MsgID, r.Item, r.Kind)
	d := &s.detail
	if d.key == key {
		return nil
	}
	d.cancelLoad()
	d.key, d.row, d.msg, d.expanded, d.lines = key, *r, s.findMsg(r.MsgID), false, nil
	d.sections = headerSections(*r, d.msg)
	if !needsLoad(*r, d.msg) {
		return nil
	}
	d.loading = true
	ctx, cancel := context.WithCancel(s.ctx)
	d.cancel = cancel
	d.gen = a.nextGen()
	return d.fetch(a, ctx, false)
}

func (d *detailPane) expand(a *App) tea.Cmd {
	if d.expanded {
		return nil
	}
	d.expanded, d.lines = true, nil
	if !needsLoad(d.row, d.msg) {
		return nil
	}
	d.cancelLoad()
	ctx, cancel := context.WithCancel(a.ctx)
	d.cancel = cancel
	d.gen = a.nextGen()
	d.loading = true
	return d.fetch(a, ctx, true)
}

func needsLoad(r trace.Row, m *model.Message) bool { return view.NeedsLoad(r, m) }

func (d *detailPane) fetch(a *App, ctx context.Context, full bool) tea.Cmd {
	gen, key, r, m, st := d.gen, d.key, d.row, d.msg, a.st
	return func() tea.Msg {
		secs, truncated, err := view.LoadDetail(ctx, st, r, m, full)
		if err != nil {
			return errMsg{gen, err}
		}
		out := toSections(secs)
		if truncated {
			out = append(out, section{title: "truncated", body: "Large content was cut at " + trace.Size(view.DetailCap) + ". Press e to expand."})
		}
		return detailLoadedMsg{gen, key, out}
	}
}

func headerSections(r trace.Row, m *model.Message) []section {
	return toSections([]view.Section{view.Header(r, m)})
}

func toSections(in []view.Section) []section {
	out := make([]section, len(in))
	for i, s := range in {
		body := s.Body
		if s.KV != nil {
			var b strings.Builder
			for _, kv := range s.KV {
				v := strings.ReplaceAll(kv[1], "\n", "\n           ")
				fmt.Fprintf(&b, "%-10s %s\n", kv[0], v)
			}
			body = b.String()
		}
		out[i] = section{title: s.Title, body: body, diff: s.Diff}
	}
	return out
}

func (d *detailPane) view(a *App, w, h, off int) string {
	if d.key == "" {
		return a.s.dim.Render("select a row")
	}
	if d.lines == nil || d.lineW != w {
		d.lineW = w
		d.lines = d.render(a, w)
	}
	lines := d.lines
	if d.loading {
		lines = append(append([]string{}, lines...), a.s.dim.Render("loading…"))
	}
	off = max(0, min(off, len(lines)-h))
	end := min(len(lines), off+h)
	return strings.Join(lines[off:end], "\n")
}

func (d *detailPane) render(a *App, w int) []string {
	var out []string
	for i, sec := range d.sections {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, a.s.bold.Render("── "+sec.title+" "+strings.Repeat("─", max(0, w-len([]rune(sec.title))-4))))
		for _, l := range textutil.Wrap(sec.body, w) {
			if sec.diff {
				switch {
				case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
					l = a.s.diffAdd.Render(l)
				case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
					l = a.s.diffDel.Render(l)
				case strings.HasPrefix(l, "@@"):
					l = a.s.diffHunk.Render(l)
				}
			}
			out = append(out, l)
		}
	}
	return out
}
