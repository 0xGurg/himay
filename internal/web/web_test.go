package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	b := fixture.Standard(t)
	st, err := store.Open(context.Background(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, s *Server, path string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7878"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body), rec.Header()
}

func TestRoutes(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		path string
		code int
		want []string
	}{
		{"/", 302, nil},
		{"/p/p1", 200, []string{"Fix layout", "↳1", "Himay"}},
		{"/p/p2?archived=1", 200, []string{"Old session"}},
		{"/s/ses_root", 200, []string{"Fix layout", "Fix the layout please", "opencode-go/glm-5.2", "Trace", "data-child=\"/s/ses_child\""}},
		{"/s/ses_child", 200, []string{"subagent of Fix layout"}},
		{"/s/ses_root/trace?kind=error", 200, []string{"quota exceeded"}},
		{"/s/ses_root/trace?failed=1", 200, []string{"File not found"}},
		{"/s/ses_root/trace?q=pnpm", 200, []string{"match “pnpm”"}},
		{"/s/ses_root/trace?q=layout&deep=1", 200, []string{"match"}},
		{"/s/ses_root/trace?fold=all", 200, []string{"▸"}},
		{"/s/ses_root/instructions", 200, []string{"recorded", "AGENTS.md", "strong", "textual overlap"}},
		{"/s/ses_old/instructions", 200, []string{"stored no instruction records"}},
		{"/s/ses_root/usage", 200, []string{"By model", "openai/gpt-5.6-luna"}},
		{"/s/ses_root/files", 200, []string{"src/app.tsx"}},
		{"/children/ses_root", 200, []string{"explore layout"}},
		{"/search?q=layout", 200, []string{"Fix layout"}},
		{"/search?q=pnpm&deep=1", 200, []string{"<mark>"}},
		{"/s/nope", 404, []string{"not found"}},
		{"/s/ses_root/row?i=999999", 404, nil},
		{"/static/app.css", 200, nil},
		{"/static/htmx.min.js", 200, nil},
	}
	for _, c := range cases {
		code, body, _ := get(t, s, c.path)
		if code != c.code {
			t.Errorf("%s: code %d, want %d\n%s", c.path, code, c.code, trunc(body))
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q\n%s", c.path, w, trunc(body))
			}
		}
	}
}

func TestEveryRowDetailRenders(t *testing.T) {
	s := newTestServer(t)
	l, err := s.session(context.Background(), "ses_root")
	if err != nil {
		t.Fatal(err)
	}
	for i := range l.rows {
		code, body, _ := get(t, s, "/s/ses_root/row?i="+itoa(i))
		if code != 200 {
			t.Errorf("row %d (%s): %d %s", i, l.rows[i].Kind, code, trunc(body))
		}
	}
	// Diff, encrypted reasoning, truncation, and escaping of untrusted tool output.
	var sawDiff, sawEnc, sawTrunc bool
	for i, r := range l.rows {
		_, body, _ := get(t, s, "/s/ses_root/row?i="+itoa(i))
		sawDiff = sawDiff || strings.Contains(body, `class="da"`)
		sawEnc = sawEnc || strings.Contains(body, "cannot decrypt")
		if r.Kind.String() == "user" && strings.Contains(body, "truncated") {
			sawTrunc = strings.Contains(body, "full=1")
		}
		if strings.Contains(body, "\x1b[") {
			t.Errorf("row %d leaks raw ANSI escapes", i)
		}
	}
	if !sawDiff || !sawEnc || !sawTrunc {
		t.Errorf("diff=%v encrypted=%v truncated+expand=%v", sawDiff, sawEnc, sawTrunc)
	}
}

func TestReadOnlyAndLocalOnly(t *testing.T) {
	s := newTestServer(t)
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(m, "/s/ses_root", nil)
		req.Host = "127.0.0.1:7878"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	for _, host := range []string{"evil.example", "192.168.1.5:7878", "attacker.localhost.evil:80"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("host %s: %d", host, rec.Code)
		}
	}
	for _, host := range []string{"localhost:7878", "127.0.0.1", "[::1]:7878"} {
		req := httptest.NewRequest("GET", "/static/app.css", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("host %s: %d", host, rec.Code)
		}
	}
	_, _, h := get(t, s, "/s/ses_root")
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	if h.Get("Cache-Control") != "no-store" {
		t.Errorf("session pages must not be cached")
	}
	if _, err := Listen("0.0.0.0:0"); err == nil {
		t.Error("Listen accepted a non-loopback address")
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestNoInlineStyleOrScript(t *testing.T) {
	// The CSP forbids inline styles and scripts; make sure templates don't rely on them.
	s := newTestServer(t)
	for _, p := range []string{"/p/p1", "/s/ses_root", "/s/ses_root/instructions", "/s/ses_root/usage"} {
		_, body, _ := get(t, s, p)
		for _, bad := range []string{` style="`, "<script>", " onclick=", " onload="} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q (blocked by CSP)", p, bad)
			}
		}
	}
}

func TestTracePaging(t *testing.T) {
	b := fixture.New(t)
	b.Project("p", "/w")
	b.Session(fixture.Session{ID: "big", Project: "p", Title: "big"})
	for i := 0; i < 900; i++ {
		b.Msg("big", "u"+itoa(i), "user", `{"text":"hello `+itoa(i)+`","time":{"created":1}}`)
	}
	st, err := store.Open(context.Background(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, _ := New(st, Options{})
	_, body, _ := get(t, s, "/s/big")
	if strings.Count(body, `class="row `) != tracePage || !strings.Contains(body, "offset=400") {
		t.Fatalf("first page rows=%d", strings.Count(body, `class="row `))
	}
	_, body, _ = get(t, s, "/s/big/trace?offset=800")
	if n := strings.Count(body, `class="row `); n != 100 || strings.Contains(body, "offset=") {
		t.Fatalf("last page rows=%d", n)
	}
}

func trunc(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
