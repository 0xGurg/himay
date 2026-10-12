// Package tui is Himay's Bubble Tea interface: a stack of screens over a
// read-only store. All database access happens inside tea.Cmds carrying a
// cancellable context; stale results are dropped by generation number.
package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/textutil"
)

// screen is one level of the navigation stack.
type screen interface {
	update(a *App, msg tea.Msg) tea.Cmd
	view(a *App, w, h int) string
	// capturing reports whether the screen is consuming raw key input
	// (e.g. a text prompt), so global keys like q must not fire.
	capturing() bool
	// close cancels any in-flight work owned by the screen.
	close()
}

// Options configures the TUI.
type Options struct {
	SessionID string // open this session directly
	ASCII     bool   // use "Himay" instead of the Baybayin title
}

// App is the root Bubble Tea model.
type App struct {
	st     *store.Store
	ctx    context.Context
	opts   Options
	stack  []screen
	w, h   int
	dark   bool
	s      styles
	gen    uint64 // monotonically increasing request generation
	status string
	err    error
}

// New builds the root model.
func New(ctx context.Context, st *store.Store, opts Options) *App {
	a := &App{st: st, ctx: ctx, opts: opts, dark: true, s: newStyles(true)}
	a.stack = []screen{newBrowser()}
	return a
}

func (a *App) nextGen() uint64 { a.gen++; return a.gen }

func (a *App) push(s screen) tea.Cmd {
	a.stack = append(a.stack, s)
	if i, ok := s.(interface{ init(*App) tea.Cmd }); ok {
		return i.init(a)
	}
	return nil
}

func (a *App) pop() {
	if len(a.stack) <= 1 {
		return
	}
	a.stack[len(a.stack)-1].close()
	a.stack = a.stack[:len(a.stack)-1]
}

func (a *App) top() screen { return a.stack[len(a.stack)-1] }

func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, a.stack[0].(*browser).init(a)}
	if a.opts.SessionID != "" {
		cmds = append(cmds, a.push(newSessionScreen(a.opts.SessionID)))
	}
	return tea.Batch(cmds...)
}

type errMsg struct {
	gen uint64
	err error
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		a.dark = msg.IsDark()
		a.s = newStyles(a.dark)
		return a, nil
	case errMsg:
		if msg.err != nil && msg.err != context.Canceled {
			a.err = msg.err
		}
		return a, nil
	case tea.KeyPressMsg:
		a.err, a.status = nil, ""
		if msg.String() == "ctrl+c" {
			return a, a.quit()
		}
		if !a.top().capturing() && msg.String() == "q" {
			return a, a.quit()
		}
	}
	return a, a.top().update(a, msg)
}

func (a *App) quit() tea.Cmd {
	for _, s := range a.stack {
		s.close()
	}
	return tea.Quit
}

func (a *App) View() tea.View {
	var content string
	if a.w == 0 {
		content = "loading…"
	} else {
		content = a.render()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Himay"
	return v
}

func (a *App) render() string {
	header := a.header()
	foot := a.footer()
	bodyH := a.h - 2
	if bodyH < 3 {
		bodyH = 3
	}
	body := a.top().view(a, a.w, bodyH)
	return header + "\n" + fit(body, a.w, bodyH) + "\n" + foot
}

func (a *App) title() string {
	if a.opts.ASCII {
		return "Himay"
	}
	return TitleBaybayin + " Himay"
}

func (a *App) header() string {
	left := a.s.title.Render(a.title()) + a.s.dim.Render(" ─ OpenCode traces")
	right := a.s.faint.Render("read-only · " + strings.ToUpper(orDash(a.st.Caps.JournalMode)))
	gap := a.w - textutil.Width(left) - textutil.Width(right)
	if gap < 1 {
		return textutil.Truncate(left, a.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (a *App) footer() string {
	if a.err != nil {
		return a.s.fail.Render(textutil.Truncate("error: "+a.err.Error(), a.w))
	}
	hint := ""
	if h, ok := a.top().(interface{ hints() string }); ok {
		hint = h.hints()
	}
	if a.status != "" {
		hint = a.status + "   " + hint
	}
	return a.s.dim.Render(textutil.Truncate(hint, a.w))
}

// fit forces a block to exactly h lines, each at most w columns.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if textutil.Width(l) > w {
			lines[i] = textutil.Truncate(l, w)
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Run starts the TUI.
func Run(ctx context.Context, st *store.Store, opts Options) error {
	p := tea.NewProgram(New(ctx, st, opts), tea.WithContext(ctx))
	_, err := p.Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}
