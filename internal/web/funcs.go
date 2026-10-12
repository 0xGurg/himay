package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/view"
)

var funcs = template.FuncMap{
	"glyph":       func(k trace.Kind) string { return trace.Glyph(k) },
	"kind":        func(k trace.Kind) string { return k.String() },
	"kindCls":     kindClass,
	"elapsed":     trace.Elapsed,
	"dur":         trace.Duration,
	"count":       trace.Count,
	"size":        trace.Size,
	"age":         age,
	"date":        func(t time.Time) string { return t.Format("Jan 2, 2006 15:04") },
	"esc":         url.PathEscape,
	"qesc":        url.QueryEscape,
	"model":       func(m model.ModelRef) string { return m.String() },
	"evidence":    func(e model.Evidence) string { return e.String() },
	"isStep":      func(k trace.Kind) bool { return k == trace.KStep },
	"isUser":      func(k trace.Kind) bool { return k == trace.KUser },
	"ok":          func(s trace.Status) bool { return s == trace.SOK },
	"bad":         func(s trace.Status) bool { return s == trace.SFail },
	"running":     func(s trace.Status) bool { return s == trace.SRunning },
	"derived":     func(e model.Evidence) bool { return e == model.Derived },
	"recorded":    func(e model.Evidence) bool { return e == model.Recorded },
	"strong":      func(e model.Evidence) bool { return e == model.Strong },
	"projectName": projectName,
	"title":       sessionTitle,
	"pct":         func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"money":       func(f float64) string { return fmt.Sprintf("$%.4f", f) },
	"money2":      func(f float64) string { return fmt.Sprintf("$%.2f", f) },
	"tok": func(l bool, v int64) string {
		if !l {
			return "—"
		}
		return trace.Count(v)
	},
	"diffLines": diffLines,
	"lines":     func(s string) []string { return strings.Split(s, "\n") },
	"rel":       view.RelPath,
	"base": func(p string) string {
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return p[i+1:]
		}
		return p
	},
	"filterURL": filterURL,
	"has":       func(m map[string]bool, k string) bool { return m[k] },
	"groups":    func() []view.FilterGroup { return view.FilterGroups },
	"asset":     func(name string) string { return "/static/" + name + "?v=" + assetVersion },
	"dict2":     func(s model.SessionSummary, current string) sessItem { return sessItem{S: s, Current: current} },
	"add":       func(a, b int) int { return a + b },
	"mark":      markMatch,
	"short": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
}

func kindClass(k trace.Kind) string {
	switch k {
	case trace.KUser:
		return "k-user"
	case trace.KStep:
		return "k-step"
	case trace.KReasoning, trace.KReasoningUnavailable:
		return "k-reasoning"
	case trace.KTool, trace.KShell:
		return "k-tool"
	case trace.KSubagent, trace.KSkill, trace.KInstructionLoaded, trace.KInstructionUpdated:
		return "k-accent"
	case trace.KFileChange:
		return "k-file"
	case trace.KError, trace.KRetry:
		return "k-error"
	case trace.KModelSwitched, trace.KAgentSwitched, trace.KLocationSwitched, trace.KToolsChanged, trace.KCompaction:
		return "k-warn"
	}
	return "k-muted"
}

type sessItem struct {
	S       model.SessionSummary
	Current string
}

type diffLine struct{ Cls, Text string }

func diffLines(s string) []diffLine {
	var out []diffLine
	for _, l := range strings.Split(s, "\n") {
		c := ""
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---"):
			c = "dh"
		case strings.HasPrefix(l, "+"):
			c = "da"
		case strings.HasPrefix(l, "-"):
			c = "dr"
		case strings.HasPrefix(l, "@@"):
			c = "dh"
		}
		out = append(out, diffLine{c, l})
	}
	return out
}

// markMatch highlights needle inside text (HTML-escaped).
func markMatch(text, needle string) template.HTML {
	esc := template.HTMLEscapeString
	if needle == "" {
		return template.HTML(esc(text))
	}
	n := strings.Trim(needle, "`")
	i := strings.Index(strings.ToLower(text), strings.ToLower(n))
	if i < 0 {
		return template.HTML(esc(text))
	}
	return template.HTML(esc(text[:i]) + "<mark>" + esc(text[i:i+len(n)]) + "</mark>" + esc(text[i+len(n):]))
}

// filterURL builds the trace URL with one filter toggled.
func filterURL(sessionID string, f view.Filter, query, toggleKind, setAgent, setModel string, toggleFailed bool) string {
	v := url.Values{}
	for k := range f.Groups {
		if k != toggleKind {
			v.Add("kind", k)
		}
	}
	if toggleKind != "" && !f.Groups[toggleKind] {
		v.Add("kind", toggleKind)
	}
	failed := f.FailedOnly != toggleFailed
	if failed {
		v.Set("failed", "1")
	}
	agent := f.Agent
	if setAgent != "" {
		agent = setAgent
		if f.Agent == setAgent {
			agent = ""
		}
	}
	if agent != "" {
		v.Set("agent", agent)
	}
	mdl := f.Model
	if setModel != "" {
		mdl = setModel
		if f.Model == setModel {
			mdl = ""
		}
	}
	if mdl != "" {
		v.Set("model", mdl)
	}
	if query != "" {
		v.Set("q", query)
	}
	return "/s/" + url.PathEscape(sessionID) + "/trace?" + v.Encode()
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

func sessionTitle(s model.SessionSummary) string {
	if s.Title != "" {
		return s.Title
	}
	return s.ID
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
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2006")
}
