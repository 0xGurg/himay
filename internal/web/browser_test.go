package web

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/store"
)

// TestBrowserInteraction drives a real Chrome against the fixture to verify
// htmx wiring and keyboard navigation. Set HIMAY_BROWSER_TEST=1 to run.
func TestBrowserInteraction(t *testing.T) {
	if os.Getenv("HIMAY_BROWSER_TEST") == "" {
		t.Skip("set HIMAY_BROWSER_TEST=1 (requires Chrome)")
	}
	s := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, ln)
	base := "http://" + ln.Addr().String()

	actx, acancel := chromedp.NewExecAllocator(ctx, append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1600, 1000))...)
	defer acancel()
	bctx, bcancel := chromedp.NewContext(actx)
	defer bcancel()
	bctx, tcancel := context.WithTimeout(bctx, 60*time.Second)
	defer tcancel()

	do := func(steps ...chromedp.Action[chromedp.Void]) {
		t.Helper()
		if err := chromedp.Do(bctx, steps...); err != nil {
			t.Fatal(err)
		}
	}
	text := func(sel string) string {
		t.Helper()
		v, err := chromedp.Run(bctx, chromedp.Text(sel))
		if err != nil {
			t.Fatalf("text %s: %v", sel, err)
		}
		return v
	}
	// waitFor polls a JS condition; chromedp's visibility checks are unreliable
	// for elements inside sticky, scrollable panes.
	waitFor := func(js string) {
		t.Helper()
		for i := 0; i < 100; i++ {
			v, err := chromedp.Run(bctx, chromedp.Evaluate[bool](js))
			if err == nil && v {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", js)
	}
	has := func(sel string) string { return "!!document.querySelector(" + strconvQuote(sel) + ")" }
	eval := func(js string) bool {
		t.Helper()
		v, err := chromedp.Run(bctx, chromedp.Evaluate[bool](js))
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		return v
	}

	do(chromedp.Navigate(base + "/s/ses_root"))
	waitFor(has(`#rows .row`))
	// Click a tool row: detail loads via htmx.
	eval(`(document.querySelector('#rows .row.k-tool').scrollIntoView({block: 'center'}), true)`)
	do(chromedp.Click(`#rows .row.k-tool`, chromedp.NodeVisible))
	waitFor(has(`#detail .sec h3`))
	detail := text(`#detail`)
	// Keyboard: j moves the selection.
	do(chromedp.KeyEvent("j"), chromedp.Sleep(300*time.Millisecond))
	selected := eval(`!!document.querySelector('#rows .row[aria-selected="true"]')`)
	// h folds the current step: its children get hidden.
	do(chromedp.KeyEvent("h"))
	foldedHidden := eval(`(() => { const s = document.querySelector('#rows .row[aria-selected="true"]'); return !!s && s.dataset.folded === "1" && s.nextElementSibling.hidden; })()`)
	// Filter chip swaps #trace.
	do(chromedp.Click(`a.chip[href*="kind=error"]`, chromedp.NodeVisible))
	waitFor(has(`#rows .row.k-error`) + " && !document.querySelector('#rows .row.k-user')")
	traceText := text(`#trace`)
	// Instructions tab via keyboard.
	do(chromedp.KeyEvent("i"))
	waitFor(has(`details.src`))
	instr := text(`#view`)
	// Subagent expander in the session list.
	do(chromedp.Navigate(base+"/p/p1"), chromedp.Click(`[data-toggle-kids]`, chromedp.NodeVisible))
	waitFor(has(`.kids li`))
	kids := text(`.kids`)

	if !strings.Contains(strings.ToLower(detail), "input") {
		t.Errorf("detail did not load tool input: %q", detail)
	}
	if !selected {
		t.Error("j did not select a row")
	}
	if !foldedHidden {
		t.Error("h did not fold the step")
	}
	if !strings.Contains(traceText, "quota exceeded") || strings.Contains(traceText, "Fix the layout please") {
		t.Errorf("error filter not applied: %q", trunc(traceText))
	}
	if !strings.Contains(instr, "recorded") || !strings.Contains(instr, "AGENTS.md") {
		t.Errorf("instructions tab: %q", trunc(instr))
	}
	if !strings.Contains(kids, "explore layout") {
		t.Errorf("subagent expand: %q", kids)
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }

// TestDetailModal checks the detail pane is a modal below 2xl and inline above.
func TestDetailModal(t *testing.T) {
	if os.Getenv("HIMAY_BROWSER_TEST") == "" {
		t.Skip("set HIMAY_BROWSER_TEST=1 (requires Chrome)")
	}
	s := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(t.Context(), ln)
	base := "http://" + ln.Addr().String()

	type ops struct {
		do   func(...chromedp.Action[chromedp.Void])
		eval func(string) bool
		wait func(string)
	}
	run := func(width int, check func(o ops)) {
		actx, c1 := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(width, 900))...)
		defer c1()
		ctx, c2 := chromedp.NewContext(actx)
		defer c2()
		ctx, c3 := context.WithTimeout(ctx, 40*time.Second)
		defer c3()
		var o ops
		o.do = func(steps ...chromedp.Action[chromedp.Void]) {
			t.Helper()
			if err := chromedp.Do(ctx, steps...); err != nil {
				t.Fatal(err)
			}
		}
		o.eval = func(js string) bool {
			v, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js))
			if err != nil {
				t.Fatalf("eval %s: %v", js, err)
			}
			return v
		}
		o.wait = func(js string) {
			t.Helper()
			for i := 0; i < 80; i++ {
				if o.eval(js) {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatalf("@%d timed out: %s", width, js)
		}
		o.do(chromedp.Navigate(base + "/s/ses_root"))
		o.wait(`!!document.querySelector('#rows .row')`)
		check(o)
	}

	const isModal = `document.querySelector('#detail-pane').matches(':modal')`
	const isOpen = `document.querySelector('#detail-pane').open`

	run(1100, func(o ops) {
		if o.eval(isOpen) {
			t.Error("@1100 detail should start closed")
		}
		o.eval(`(document.querySelector('#rows .row.k-tool').scrollIntoView({block: 'center'}), true)`)
		o.do(chromedp.Click(`#rows .row.k-tool`, chromedp.NodeVisible))
		o.wait(isModal + ` && !!document.querySelector('#detail .sec h3')`)
		if !o.eval(`document.querySelector('#detail').textContent.includes('read')`) {
			t.Error("@1100 modal did not show the clicked row")
		}
		o.do(chromedp.KeyEvent("j"))
		o.wait(`document.querySelector('#rows .row[aria-selected="true"]').dataset.idx !== "5"`)
		if !o.eval(isModal) {
			t.Error("@1100 j should keep the modal open")
		}
		o.do(chromedp.KeyEvent(kb.Escape))
		o.wait(`!` + isOpen)
		o.do(chromedp.KeyEvent(kb.Enter)) // reopen from the focused row
		o.wait(isModal)
		o.do(chromedp.KeyEvent(kb.Enter)) // Enter inside the modal must not dismiss it
		time.Sleep(200 * time.Millisecond)
		if !o.eval(isModal) {
			t.Error("@1100 Enter inside the modal closed it")
		}
		o.do(chromedp.Click(`[data-close-detail]`, chromedp.NodeVisible))
		o.wait(`!` + isOpen)
	})

	run(1700, func(o ops) {
		o.wait(isOpen)
		if o.eval(isModal) {
			t.Error("@1700 detail should be inline, not modal")
		}
		o.eval(`(document.querySelector('#rows .row.k-tool').scrollIntoView({block: 'center'}), true)`)
		o.do(chromedp.Click(`#rows .row.k-tool`, chromedp.NodeVisible))
		o.wait(`!!document.querySelector('#detail .sec h3')`)
		if o.eval(isModal) {
			t.Error("@1700 clicking a row should not open a modal")
		}
	})
}

// TestFullHeightLayout checks the desktop shell fits the viewport and panes scroll inside.
func TestFullHeightLayout(t *testing.T) {
	if os.Getenv("HIMAY_BROWSER_TEST") == "" {
		t.Skip("set HIMAY_BROWSER_TEST=1 (requires Chrome)")
	}
	b := fixture.New(t)
	b.Project("p", "/w")
	b.Session(fixture.Session{ID: "big", Project: "p", Title: "big session"})
	for i := 0; i < 900; i++ {
		b.Msg("big", "u"+itoa(i), "user", `{"text":"hello `+itoa(i)+`","time":{"created":1}}`)
	}
	for i := 0; i < 80; i++ {
		b.Session(fixture.Session{ID: "s" + itoa(i), Project: "p", Title: "session " + itoa(i)})
	}
	st, err := store.Open(t.Context(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, _ := New(st, Options{})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(t.Context(), ln)
	base := "http://" + ln.Addr().String()

	for _, w := range []int{1280, 1700} {
		actx, c1 := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(w, 800))...)
		ctx, c2 := chromedp.NewContext(actx)
		ctx, c3 := context.WithTimeout(ctx, 40*time.Second)
		eval := func(js string) bool {
			v, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js))
			if err != nil {
				t.Fatalf("@%d eval %s: %v", w, js, err)
			}
			return v
		}
		wait := func(js string) {
			for i := 0; i < 80; i++ {
				if eval(js) {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatalf("@%d timed out: %s", w, js)
		}
		if err := chromedp.Do(ctx, chromedp.Navigate(base+"/s/big")); err != nil {
			t.Fatal(err)
		}
		wait(`document.querySelectorAll('#rows .row').length === 400`)
		if !eval(`document.documentElement.scrollHeight <= innerHeight + 1`) {
			t.Errorf("@%d page scrolls (scrollHeight > viewport); panes should scroll instead", w)
		}
		if !eval(`(() => { const r = document.querySelector('.hints').getBoundingClientRect(); return r.bottom <= innerHeight && r.top > 0; })()`) {
			t.Errorf("@%d bottom hints are not visible in the viewport", w)
		}
		if !eval(`(() => { const v = document.querySelector('#view'); return v.scrollHeight > v.clientHeight; })()`) {
			t.Errorf("@%d trace pane does not scroll internally", w)
		}
		// Scroll the trace pane to the bottom: the intersect sentinel loads the next page.
		eval(`(document.querySelector('#view').scrollTop = 1e9, true)`)
		wait(`document.querySelectorAll('#rows .row').length > 400`)
		if !eval(`(() => { const v = document.querySelector('nav.pane .pane-scroll'); return v.scrollHeight > v.clientHeight; })()`) {
			t.Errorf("@%d session rail does not scroll internally", w)
		}
		// Modal (below 2xl) overlays the viewport, centered, regardless of pane scroll.
		if w < 1536 {
			if err := chromedp.Do(ctx, chromedp.Click(`#rows .row`, chromedp.NodeVisible)); err != nil {
				t.Fatal(err)
			}
			wait(`document.querySelector('#detail-pane').matches(':modal')`)
			if !eval(`(() => { const r = document.querySelector('#detail-pane').getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight && getComputedStyle(document.querySelector('#detail-pane')).position === 'fixed'; })()`) {
				t.Errorf("@%d modal is not overlaying the viewport", w)
			}
		} else if !eval(`(() => { const r = document.querySelector('#detail-pane').getBoundingClientRect(); return r.bottom <= innerHeight && r.height > 200; })()`) {
			t.Errorf("@%d inline detail pane does not fill the column", w)
		}
		c3()
		c2()
		c1()
	}
}

// TestThemeSwitch checks system/light/dark selection, persistence, and OS override.
func TestThemeSwitch(t *testing.T) {
	if os.Getenv("HIMAY_BROWSER_TEST") == "" {
		t.Skip("set HIMAY_BROWSER_TEST=1 (requires Chrome)")
	}
	s := newTestServer(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(t.Context(), ln)
	base := "http://" + ln.Addr().String()

	actx, c1 := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1280, 800))...)
	defer c1()
	ctx, c2 := chromedp.NewContext(actx)
	defer c2()
	ctx, c3 := context.WithTimeout(ctx, 40*time.Second)
	defer c3()
	str := func(js string) string {
		v, err := chromedp.Run(ctx, chromedp.Evaluate[string](js))
		if err != nil {
			t.Fatalf("eval %s: %v", js, err)
		}
		return v
	}
	do := func(steps ...chromedp.Action[chromedp.Void]) {
		t.Helper()
		if err := chromedp.Do(ctx, steps...); err != nil {
			t.Fatal(err)
		}
	}
	bg := `getComputedStyle(document.body).backgroundColor`
	pressed := `document.querySelector('[data-theme-set][aria-pressed="true"]').dataset.themeSet`
	const light, dark = "rgb(243, 245, 248)", "rgb(15, 20, 27)"

	for _, scheme := range []string{"light", "dark"} {
		do(emulateScheme(scheme), chromedp.Navigate(base+"/s/ses_root"), chromedp.Sleep(300*time.Millisecond))
		do(chromedp.Evaluate[chromedp.Void](`localStorage.clear()`), chromedp.Reload(), chromedp.Sleep(300*time.Millisecond))
		want := map[string]string{"light": light, "dark": dark}
		if got := str(bg); got != want[scheme] {
			t.Errorf("system with OS %s: bg %s, want %s", scheme, got, want[scheme])
		}
		if p := str(pressed); p != "system" {
			t.Errorf("default pressed = %s", p)
		}
		// Explicit choices override the OS in both directions and persist across reload.
		for _, pick := range []string{"light", "dark"} {
			do(chromedp.Click(`[data-theme-set="`+pick+`"]`, chromedp.NodeVisible), chromedp.Sleep(100*time.Millisecond))
			if got := str(bg); got != want[pick] {
				t.Errorf("OS %s, picked %s: bg %s", scheme, pick, got)
			}
			do(chromedp.Reload(), chromedp.Sleep(300*time.Millisecond))
			if got, p := str(bg), str(pressed); got != want[pick] || p != pick {
				t.Errorf("OS %s, picked %s, after reload: bg %s pressed %s", scheme, pick, got, p)
			}
		}
		do(chromedp.Click(`[data-theme-set="system"]`, chromedp.NodeVisible), chromedp.Sleep(100*time.Millisecond))
		if got := str(bg); got != want[scheme] {
			t.Errorf("back to system with OS %s: bg %s", scheme, got)
		}
		if v := str(`localStorage.getItem('himay-theme') || ""`); v != "" {
			t.Errorf("system should clear the saved choice, got %q", v)
		}
	}
}

// emulateScheme sets the emulated OS color scheme (prefers-color-scheme).
func emulateScheme(scheme string) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}},
		})
		return err
	})
}

// TestHLNavigation checks context-sensitive h/l in the browser.
func TestHLNavigation(t *testing.T) {
	if os.Getenv("HIMAY_BROWSER_TEST") == "" {
		t.Skip("set HIMAY_BROWSER_TEST=1 (requires Chrome)")
	}
	s := newTestServer(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(t.Context(), ln)
	base := "http://" + ln.Addr().String()
	actx, c1 := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1200, 900))...)
	defer c1()
	ctx, c2 := chromedp.NewContext(actx)
	defer c2()
	ctx, c3 := context.WithTimeout(ctx, 40*time.Second)
	defer c3()
	do := func(steps ...chromedp.Action[chromedp.Void]) {
		t.Helper()
		if err := chromedp.Do(ctx, steps...); err != nil {
			t.Fatal(err)
		}
	}
	eval := func(js string) bool {
		v, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js))
		if err != nil {
			t.Fatalf("eval %s: %v", js, err)
		}
		return v
	}
	wait := func(js string) {
		t.Helper()
		for i := 0; i < 80; i++ {
			if eval(js) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", js)
	}
	key := func(k string) { do(chromedp.KeyEvent(k), chromedp.Sleep(150*time.Millisecond)) }
	selectRow := func(sel string) {
		eval(`(() => { const r = document.querySelector(` + strconvQuote(sel) + `); document.querySelectorAll('#rows .row[aria-selected="true"]').forEach(x => x.setAttribute('aria-selected','false')); r.setAttribute('aria-selected','true'); r.scrollIntoView({block:'center'}); return true; })()`)
	}
	cur := `document.querySelector('#rows .row[aria-selected="true"]')`

	do(chromedp.Navigate(base + "/s/ses_root"))
	wait(`!!document.querySelector('#rows .row')`)

	// Open step: h folds, second h stays; l expands, second l enters first child.
	selectRow(`#rows .row[data-step]`)
	key("h")
	if !eval(cur + `.dataset.folded === "1"`) {
		t.Fatal("h on an open step should fold it")
	}
	key("h")
	if !eval(`location.pathname === "/s/ses_root"`) {
		t.Fatal("h on a folded step must not navigate away")
	}
	key("l")
	if eval(cur + `.dataset.folded === "1"`) {
		t.Fatal("l on a folded step should expand it")
	}
	key("l")
	if eval(cur + `.hasAttribute('data-step')`) {
		t.Fatal("l on an open step should move into its first child")
	}
	// Child: h folds the parent step and selects it.
	key("h")
	if !eval(cur + `.hasAttribute('data-step') && ` + cur + `.dataset.folded === "1"`) {
		t.Fatal("h on a child should fold its step and select it")
	}
	key("l")
	// Tool row: l opens the modal; h closes it.
	selectRow(`#rows .row.k-tool`)
	key("l")
	wait(`document.querySelector('#detail-pane').matches(':modal')`)
	key("h")
	wait(`!document.querySelector('#detail-pane').open`)
	// Subagent row: l opens the child; top-level h goes to the parent.
	selectRow(`#rows .row[data-child]`)
	key("l")
	wait(`location.pathname === "/s/ses_child"`)
	wait(`!!document.querySelector('#rows .row')`)
	selectRow(`#rows .row.d0`)
	key("h")
	wait(`location.pathname === "/s/ses_root"`)
}
