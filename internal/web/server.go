// Package web serves Himay's read-only browser UI: Go templates enhanced with
// htmx, styled by a Tailwind build that is committed and embedded. It binds to
// loopback, accepts only GET/HEAD, and loads nothing from the internet.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Options configures the web server.
type Options struct {
	Addr  string // host:port; host must be loopback
	ASCII bool
}

// Server is the HTTP handler plus its session cache.
type Server struct {
	st    *store.Store
	opts  Options
	tmpl  *template.Template
	cache *sessionCache
	mux   *http.ServeMux
}

// New builds the server. It does not listen.
func New(st *store.Store, opts Options) (*Server, error) {
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("templates: %w", err)
	}
	s := &Server{st: st, opts: opts, tmpl: t, cache: newSessionCache(8), mux: http.NewServeMux()}
	static, _ := fs.Sub(staticFS, "static")
	// Asset URLs carry ?v=<content hash> (see assetVersion), so a new build always
	// gets fresh CSS/JS and old cached copies can never pair with new markup.
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheAssets(http.FileServerFS(static))))
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /p/{project}", s.handleProject)
	s.mux.HandleFunc("GET /p/{project}/sessions", s.handleSessionsFragment)
	s.mux.HandleFunc("GET /children/{session}", s.handleChildren)
	s.mux.HandleFunc("GET /search", s.handleSearch)
	s.mux.HandleFunc("GET /s/{session}", s.handleSession)
	s.mux.HandleFunc("GET /s/{session}/trace", s.handleTrace)
	s.mux.HandleFunc("GET /s/{session}/row", s.handleRow)
	s.mux.HandleFunc("GET /s/{session}/instructions", s.handleInstructions)
	s.mux.HandleFunc("GET /s/{session}/usage", s.handleUsage)
	s.mux.HandleFunc("GET /s/{session}/files", s.handleFiles)
	s.mux.HandleFunc("GET /s/{session}/search", s.handleDeepSearch)
	return s, nil
}

// ServeHTTP applies the read-only, local-only guards to every request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Himay is read-only", http.StatusMethodNotAllowed)
		return
	}
	if !loopbackHost(r.Host) {
		// Blocks DNS-rebinding: a hostile page cannot reach us via its own hostname.
		http.Error(w, "Himay only answers on localhost", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	if !strings.HasPrefix(r.URL.Path, "/static/") {
		h.Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cacheAssets(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") == assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

// assetVersion is a short hash of every embedded static file.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err == nil {
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// Listen validates the address and returns a listener bound to loopback.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if !loopbackHost(host) && host != "" {
		return nil, fmt.Errorf("refusing to listen on %q: Himay serves private session data and binds to loopback only", host)
	}
	if host == "" {
		addr = "127.0.0.1" + addr
	}
	return net.Listen("tcp", addr)
}

// Serve runs the server until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("himay web: render %s: %v", name, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // client navigated away
	}
	code := http.StatusInternalServerError
	if strings.Contains(err.Error(), "not found") {
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	s.render(w, "error", map[string]any{"Err": err.Error(), "Frag": isHX(r)})
}

func isHX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// ---------- session cache ----------

// loaded is a fully decoded session, reused across fragment requests.
type loaded struct {
	sum     model.SessionSummary
	msgs    []model.Message
	rows    []trace.Row
	updated int64
}

type sessionCache struct {
	mu    sync.Mutex
	max   int
	order []string
	m     map[string]*loaded
}

func newSessionCache(max int) *sessionCache {
	return &sessionCache{max: max, m: map[string]*loaded{}}
}

// session returns the decoded session, reloading when OpenCode has written to it.
func (s *Server) session(ctx context.Context, id string) (*loaded, error) {
	sum, err := s.st.Session(ctx, id)
	if err != nil {
		return nil, err
	}
	_, upd, err := s.st.MaxSeq(ctx, id)
	if err != nil {
		return nil, err
	}
	stamp := max(sum.Updated.UnixMilli(), upd)
	c := s.cache
	c.mu.Lock()
	if l, ok := c.m[id]; ok && l.updated == stamp {
		c.touch(id)
		c.mu.Unlock()
		return l, nil
	}
	c.mu.Unlock()

	msgs, err := trace.LoadAll(ctx, s.st, id)
	if err != nil {
		return nil, err
	}
	l := &loaded{sum: sum, msgs: msgs, rows: trace.Build(msgs), updated: stamp}
	c.mu.Lock()
	c.m[id] = l
	c.touch(id)
	for len(c.order) > c.max {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
	c.mu.Unlock()
	return l, nil
}

func (c *sessionCache) touch(id string) {
	for i, v := range c.order {
		if v == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, id)
}
