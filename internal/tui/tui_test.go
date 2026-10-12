package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/textutil"
	"github.com/0xGurg/himay/internal/trace"
)

// driver runs the model headlessly, executing commands synchronously.
type driver struct {
	t *testing.T
	a *App
}

func newDriver(t *testing.T, path string, opts Options, w, h int) *driver {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := &driver{t: t, a: New(context.Background(), st, opts)}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	d.run(d.a.Init())
	return d
}

func (d *driver) run(cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 500; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg, ok := runQuick(c)
		if !ok {
			continue // timers (cursor blink, live tick) are dropped headlessly
		}
		switch m := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case tea.QuitMsg:
		default:
			if isInternal(msg) {
				continue
			}
			_, next := d.a.Update(msg)
			queue = append(queue, next)
		}
	}
}

// runQuick runs a command. Blocking timers are tolerated: the cursor blink
// resolves in ~500ms and its message is then dropped (see isInternal), so it
// never re-arms; the live-refresh tick is never started by tests.
func runQuick(c tea.Cmd) (tea.Msg, bool) {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(30 * time.Second):
		return nil, false
	}
}

// isInternal skips Bubble Tea's own terminal-query messages and timers.
func isInternal(msg tea.Msg) bool {
	switch msg.(type) {
	case liveTickMsg:
		return true
	}
	t := fmtType(msg)
	return strings.Contains(t, "backgroundColor") || strings.Contains(t, "cursor.") || strings.Contains(t, "Blink")
}

func (d *driver) send(msg tea.Msg) {
	_, cmd := d.a.Update(msg)
	d.run(cmd)
}

func (d *driver) keys(ks ...string) {
	for _, k := range ks {
		d.send(keyMsg(k))
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+f":
		return tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}
	case " ", "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func (d *driver) frame() string {
	d.t.Helper()
	out := d.a.View().Content
	lines := strings.Split(out, "\n")
	if len(lines) != d.a.h {
		d.t.Errorf("frame has %d lines, want %d", len(lines), d.a.h)
	}
	for i, l := range lines {
		if w := textutil.Width(l); w > d.a.w {
			d.t.Errorf("line %d is %d cols wide (max %d): %q", i, w, d.a.w, textutil.Sanitize(l))
		}
	}
	return out
}

func plain(s string) string { return textutil.Sanitize(strings.ReplaceAll(s, "\n", "⏎")) }

func TestFlowAtSizes(t *testing.T) {
	b := fixture.Standard(t)
	for _, w := range []int{60, 70, 100, 140} {
		t.Run("w"+itoa(w), func(t *testing.T) {
			d := newDriver(t, b.Path, Options{}, w, 30)
			f := d.frame()
			if !strings.Contains(f, "Himay") || !strings.Contains(plain(f), "p1") {
				t.Fatalf("browser missing content:\n%s", f)
			}
			d.keys("tab") // focus sessions
			d.keys("l")   // expand subagents
			if !strings.Contains(plain(d.frame()), "explore layout") {
				t.Errorf("subagent not shown after expand:\n%s", d.frame())
			}
			d.keys("enter") // open root session
			f = d.frame()
			if _, ok := d.a.top().(*sessionScreen); !ok {
				t.Fatalf("session screen not open:\n%s", f)
			}
			if !strings.Contains(plain(f), "Fix the layout please") {
				t.Errorf("trace missing user row:\n%s", f)
			}
			for i := 0; i < 20; i++ { // walk to the subagent row
				if r := d.a.top().(*sessionScreen).selected(); r != nil && r.Kind == trace.KSubagent {
					break
				}
				d.keys("j")
				d.frame()
			}
			if r := d.a.top().(*sessionScreen).selected(); r == nil || r.Child != "ses_child" {
				t.Fatalf("expected subagent row selected, got %+v", r)
			}
			d.keys("enter") // into child
			if s := d.a.top().(*sessionScreen); s.id != "ses_child" {
				t.Fatalf("child not opened: %s", s.id)
			}
			d.frame()
			d.keys("p") // back to parent
			if s := d.a.top().(*sessionScreen); s.id != "ses_root" {
				t.Fatalf("parent not restored: %s", s.id)
			}
			for _, ov := range []string{"u", "F", "i", "?", "f"} {
				d.keys(ov)
				f := d.frame()
				if strings.Contains(f, "panic") {
					t.Fatal(f)
				}
				d.keys("j", "j")
				d.frame()
				d.keys("esc")
			}
			d.keys("enter") // detail of current row
			d.frame()
			d.keys("e", "j", "esc")
			d.keys("/")
			for _, r := range "pnpm" {
				d.keys(string(r))
			}
			d.keys("enter")
			if len(d.a.top().(*sessionScreen).matches) == 0 {
				t.Error("search found nothing")
			}
			d.keys("n", "N", "esc", "z", "z", "G", "g", "h", "l")
			d.frame()
			d.keys("esc") // back to browser
			if _, ok := d.a.top().(*browser); !ok {
				t.Fatal("esc did not return to browser")
			}
		})
	}
}

func TestInstructionPanelContent(t *testing.T) {
	b := fixture.Standard(t)
	d := newDriver(t, b.Path, Options{SessionID: "ses_root"}, 140, 40)
	d.keys("i")
	f := plain(d.frame())
	for _, want := range []string{"[recorded]", "AGENTS.md", "Correlation is textual overlap", "strong"} {
		if !strings.Contains(f, want) {
			t.Errorf("instruction panel missing %q:\n%s", want, d.frame())
		}
	}
	d2 := newDriver(t, b.Path, Options{SessionID: "ses_old"}, 100, 30)
	d2.keys("i")
	if !strings.Contains(plain(d2.frame()), "stored no instruction records") {
		t.Errorf("old session should show honest empty state:\n%s", d2.frame())
	}
}

func TestDeepSearchAndCancel(t *testing.T) {
	b := fixture.Standard(t)
	d := newDriver(t, b.Path, Options{}, 120, 30)
	d.keys("ctrl+f")
	for _, r := range "explore" {
		d.keys(string(r))
	}
	d.keys("enter")
	br := d.a.top().(*browser)
	if len(br.results) == 0 {
		t.Fatalf("deep search found nothing:\n%s", d.frame())
	}
	d.frame()
	d.keys("esc")
	if br.resultsOn {
		t.Error("esc did not leave results")
	}
}

func TestASCIITitle(t *testing.T) {
	b := fixture.Standard(t)
	d := newDriver(t, b.Path, Options{ASCII: true}, 80, 20)
	if strings.Contains(d.frame(), TitleBaybayin) {
		t.Error("ascii mode rendered Baybayin")
	}
}

// TestRealDatabase renders the reference sessions from the user's real DB
// when HIMAY_REAL_DB is set. It only reads.
func TestRealDatabase(t *testing.T) {
	path := os.Getenv("HIMAY_REAL_DB")
	if path == "" {
		t.Skip("set HIMAY_REAL_DB to run against a real database")
	}
	for _, id := range strings.Fields(os.Getenv("HIMAY_REAL_SESSIONS")) {
		for _, w := range []int{70, 100, 140} {
			d := newDriver(t, path, Options{SessionID: id}, w, 40)
			f := d.frame()
			for _, k := range []string{"j", "j", "j", "enter", "esc", "u", "esc", "i", "esc", "F", "esc", "G"} {
				d.keys(k)
				f = d.frame()
			}
			if os.Getenv("HIMAY_SHOW") != "" {
				var lines []string
				for _, l := range strings.Split(f, "\n") {
					lines = append(lines, textutil.Sanitize(l))
				}
				t.Logf("%s @%d\n%s", id, w, strings.Join(lines, "\n"))
			}
		}
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }

func TestRealInstrPanel(t *testing.T) {
	path, id := os.Getenv("HIMAY_REAL_DB"), os.Getenv("HIMAY_REAL_INSTR")
	if path == "" || id == "" {
		t.Skip("set HIMAY_REAL_DB and HIMAY_REAL_INSTR")
	}
	d := newDriver(t, path, Options{SessionID: id}, 130, 34)
	d.keys("i")
	d.frame()
	d.keys("space")
	var lines []string
	for _, l := range strings.Split(d.frame(), "\n") {
		lines = append(lines, textutil.Sanitize(l))
	}
	t.Log("\n" + strings.Join(lines, "\n"))
}

func TestHLContextNavigation(t *testing.T) {
	b := fixture.Standard(t)
	d := newDriver(t, b.Path, Options{}, 120, 40)
	d.keys("tab", "enter") // open ses_root from the browser
	s := d.a.top().(*sessionScreen)
	sel := func() trace.Row { return *s.selected() }
	walkTo := func(kind trace.Kind) {
		t.Helper()
		s.cur.pos = 0
		for i := 0; i < len(s.visible); i++ {
			if sel().Kind == kind {
				return
			}
			d.keys("j")
		}
		t.Fatalf("no %s row", kind)
	}

	// Step row: h folds, a second h stays put; l expands, a second l enters the first child.
	walkTo(trace.KStep)
	step := sel()
	d.keys("h")
	if !s.collapsed[step.MsgID] || sel().MsgID != step.MsgID {
		t.Fatal("h on an open step should fold it")
	}
	d.keys("h")
	if d.a.top() != screen(s) {
		t.Fatal("h on a folded step must not leave the session")
	}
	d.keys("l")
	if s.collapsed[step.MsgID] {
		t.Fatal("l on a folded step should expand it")
	}
	d.keys("l")
	if r := sel(); r.MsgID != step.MsgID || r.Depth == 0 {
		t.Fatalf("l on an open step should enter its first child, got %+v", r)
	}

	// Child row: h folds the parent step and selects it.
	d.keys("h")
	if r := sel(); r.Kind != trace.KStep || !s.collapsed[step.MsgID] {
		t.Fatalf("h on a child should fold its step and select it, got %s", r.Kind)
	}
	d.keys("l")

	// Tool row: l opens detail; h closes it.
	walkTo(trace.KTool)
	d.keys("l")
	if s.overlay != ovDetail {
		t.Fatal("l on a tool row should open detail")
	}
	d.keys("h")
	if s.overlay != ovNone {
		t.Fatal("h should close detail")
	}

	// Subagent row: l opens the child session; h on a top-level row goes back.
	walkTo(trace.KSubagent)
	d.keys("l")
	child, ok := d.a.top().(*sessionScreen)
	if !ok || child.id != "ses_child" {
		t.Fatal("l on a subagent row should open the child session")
	}
	child.cur.pos = 0 // user row, top level
	d.keys("h")
	if back, ok := d.a.top().(*sessionScreen); !ok || back.id != "ses_root" {
		t.Fatal("h on a top-level row should go back")
	}
}
