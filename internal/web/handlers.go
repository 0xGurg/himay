package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/usage"
	"github.com/0xGurg/himay/internal/view"
)

const (
	sessionPage = 60
	tracePage   = 400
)

// page is the data common to every full page.
type page struct {
	Title     string
	ASCII     bool
	Projects  []model.Project
	ProjectID string
	Journal   string
}

func (s *Server) basePage(r *http.Request, title, projectID string) (page, error) {
	ps, err := s.st.Projects(r.Context())
	return page{Title: title, ASCII: s.opts.ASCII, Projects: ps, ProjectID: projectID, Journal: s.st.Caps.JournalMode}, err
}

// ---------- browser ----------

type sessionsData struct {
	ProjectID string
	Sessions  []model.SessionSummary
	Next      string // URL of the next page, "" when done
	Archived  bool
	Current   string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	p, err := s.basePage(r, "Himay", "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(p.Projects) > 0 {
		http.Redirect(w, r, "/p/"+url.PathEscape(p.Projects[0].ID), http.StatusFound)
		return
	}
	s.render(w, "home", p)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("project")
	p, err := s.basePage(r, "Himay", pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sd, err := s.sessionsPage(r, pid, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, pr := range p.Projects {
		if pr.ID == pid {
			p.Title = projectName(pr) + " · Himay"
		}
	}
	s.render(w, "project", struct {
		page
		Sessions sessionsData
	}{p, sd})
}

func (s *Server) handleSessionsFragment(w http.ResponseWriter, r *http.Request) {
	sd, err := s.sessionsPage(r, r.PathValue("project"), r.URL.Query().Get("current"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "session-items", sd)
}

func (s *Server) sessionsPage(r *http.Request, pid, current string) (sessionsData, error) {
	q := r.URL.Query()
	arch := q.Get("archived") == "1"
	var cur store.Cursor
	if u := q.Get("after_u"); u != "" {
		cur.Updated, _ = strconv.ParseInt(u, 10, 64)
		cur.ID = q.Get("after_id")
	}
	list, err := s.st.RootSessions(r.Context(), pid, cur, sessionPage, arch)
	if err != nil {
		return sessionsData{}, err
	}
	sd := sessionsData{ProjectID: pid, Sessions: list, Archived: arch, Current: current}
	if len(list) == sessionPage {
		last := list[len(list)-1]
		v := url.Values{"after_u": {strconv.FormatInt(last.Updated.UnixMilli(), 10)}, "after_id": {last.ID}, "current": {current}}
		if arch {
			v.Set("archived", "1")
		}
		sd.Next = "/p/" + url.PathEscape(pid) + "/sessions?" + v.Encode()
	}
	return sd, nil
}

func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	kids, err := s.st.Children(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "children", struct {
		Sessions []model.SessionSummary
		Current  string
	}{kids, r.URL.Query().Get("current")})
}

// handleSearch serves title search (default) and deep content search (deep=1),
// one chunk per request so the browser drives progress and can stop anytime.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	needle := strings.TrimSpace(q.Get("q"))
	data := struct {
		Q, Next     string
		Deep        bool
		Titles      []model.SessionSummary
		Hits        []store.Hit
		Progress    int
		Done, First bool
	}{Q: needle, Deep: q.Get("deep") == "1", First: q.Get("after") == ""}
	if needle == "" {
		s.render(w, "search-empty", nil)
		return
	}
	if !data.Deep {
		list, err := s.st.SearchTitles(r.Context(), needle, 100)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data.Titles, data.Done = list, true
		s.render(w, "search-results", data)
		return
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	maxRow, _ := strconv.ParseInt(q.Get("max"), 10, 64)
	if maxRow == 0 {
		var err error
		if maxRow, err = s.st.MaxRowID(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	hits, hi, err := s.st.DeepSearchChunk(r.Context(), needle, after, 1500)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Hits = hits
	if hi == 0 || after >= maxRow {
		data.Done, data.Progress = true, 100
	} else {
		if maxRow > 0 {
			data.Progress = int(100 * hi / maxRow)
		}
		data.Next = "/search?" + url.Values{"q": {needle}, "deep": {"1"},
			"after": {strconv.FormatInt(hi, 10)}, "max": {strconv.FormatInt(maxRow, 10)}}.Encode()
	}
	s.render(w, "search-results", data)
}

// ---------- session ----------

type rowView struct {
	trace.Row
	Idx    int
	Start  time.Time
	Folded bool
}

type traceData struct {
	SessionID string
	Rows      []rowView
	Next      string
	Total     int
	Shown     int
	Filter    view.Filter
	Query     string
	Matches   int
	Start     time.Time
	Selected  int
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	l, err := s.session(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.basePage(r, sessionTitle(l.sum)+" · Himay", l.sum.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rep := usage.Build(l.msgs)
	vs := view.ComputeStats(l.rows, l.msgs)
	td := s.traceView(r, l)
	var parent *model.SessionSummary
	if l.sum.ParentID != "" {
		if ps, err := s.st.Session(r.Context(), l.sum.ParentID); err == nil {
			parent = &ps
		}
	}
	sd, err := s.sessionsPage(r, l.sum.ProjectID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "session", struct {
		page
		Sum      model.SessionSummary
		Parent   *model.SessionSummary
		Usage    usage.Report
		Stats    sessionStats
		Trace    traceData
		Groups   []view.FilterGroup
		Sessions sessionsData
	}{p, l.sum, parent, rep, stats2(vs, rep), td, view.FilterGroups, sd})
}

type sessionStats struct {
	view.Stats
	Steps     int
	Tokens    int64
	Cost      float64
	ModelBars []bar
	MaxTokens int64
}

type bar struct {
	Key    string
	Pct    float64
	Offset float64
	Line   usage.Line
	Color  int
}

func stats2(st view.Stats, rep usage.Report) sessionStats {
	out := sessionStats{Stats: st, Steps: rep.Total.Steps, Tokens: rep.Total.Tokens.Total(), Cost: rep.Total.Cost}
	total := rep.Total.Tokens.Total()
	off := 0.0
	for i, l := range rep.ByModel {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(l.Tokens.Total()) / float64(total)
		}
		out.ModelBars = append(out.ModelBars, bar{Key: l.Key, Pct: pct, Offset: off, Line: l, Color: i % 6})
		off += pct
	}
	return out
}

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	l, err := s.session(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	td := s.traceView(r, l)
	if r.URL.Query().Get("offset") != "" {
		s.render(w, "trace-rows", td)
		return
	}
	s.render(w, "trace", td)
}

// traceView applies fold/filter/search state from the query string.
func (s *Server) traceView(r *http.Request, l *loaded) traceData {
	q := r.URL.Query()
	f := view.Filter{FailedOnly: q.Get("failed") == "1", Agent: q.Get("agent"), Model: q.Get("model")}
	for _, g := range q["kind"] {
		if f.Groups == nil {
			f.Groups = map[string]bool{}
		}
		f.Groups[g] = true
	}
	folded := map[string]bool{}
	if q.Get("fold") == "all" {
		for _, r := range l.rows {
			if r.Kind == trace.KStep && r.HasKids {
				folded[r.MsgID] = true
			}
		}
	}
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	var deep map[string]bool
	if needle != "" && q.Get("deep") == "1" {
		hits, err := s.st.SearchSession(r.Context(), l.sum.ID, needle, 5000)
		if err == nil {
			deep = map[string]bool{}
			for _, h := range hits {
				deep[h.MsgID] = true
			}
		}
	}
	start := l.sum.Created
	if len(l.msgs) > 0 && !l.msgs[0].Created.IsZero() {
		start = l.msgs[0].Created
	}
	td := traceData{SessionID: l.sum.ID, Total: len(l.rows), Filter: f, Query: q.Get("q"), Start: start}
	var vis []rowView
	skip := -1
	for i, row := range l.rows {
		if skip >= 0 && row.Depth > skip {
			continue
		}
		skip = -1
		if f.Active() && !f.Match(row) {
			continue
		}
		if needle != "" {
			hit := strings.Contains(row.Searchable(), needle)
			if deep != nil {
				hit = deep[row.MsgID]
			}
			if !hit {
				continue
			}
			td.Matches++
		}
		rv := rowView{Row: row, Idx: i, Start: start}
		if !f.Active() && needle == "" && row.Kind == trace.KStep && folded[row.MsgID] {
			rv.Folded = true
			skip = row.Depth
		}
		vis = append(vis, rv)
	}
	td.Shown = len(vis)
	off, _ := strconv.Atoi(q.Get("offset"))
	off = max(0, min(off, len(vis)))
	end := min(len(vis), off+tracePage)
	td.Rows = vis[off:end]
	if end < len(vis) {
		v := cloneQuery(q)
		v.Set("offset", strconv.Itoa(end))
		td.Next = "/s/" + url.PathEscape(l.sum.ID) + "/trace?" + v.Encode()
	}
	return td
}

func cloneQuery(q url.Values) url.Values {
	v := url.Values{}
	for k, vs := range q {
		v[k] = append([]string(nil), vs...)
	}
	return v
}

func (s *Server) handleRow(w http.ResponseWriter, r *http.Request) {
	l, err := s.session(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	idx, err := strconv.Atoi(r.URL.Query().Get("i"))
	if err != nil || idx < 0 || idx >= len(l.rows) {
		s.fail(w, r, fmt.Errorf("row not found"))
		return
	}
	row := l.rows[idx]
	full := r.URL.Query().Get("full") == "1"
	secs, truncated, err := view.LoadDetail(r.Context(), s.st, row, view.FindMsg(l.msgs, row.MsgID), full)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "detail", struct {
		SessionID string
		Row       trace.Row
		Idx       int
		Sections  []view.Section
		Truncated bool
	}{l.sum.ID, row, idx, secs, truncated})
}

func (s *Server) handleInstructions(w http.ResponseWriter, r *http.Request) {
	l, err := s.session(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pv, err := view.AnalyzeProvenance(r.Context(), s.st, l.sum.ID, l.msgs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type src struct {
		provenance.Source
		Origin           string // InstructionRecord.Source; the embedded struct shadows that name
		Label, KindLabel string
		Matches          []provenance.Match
		Strong, Possible int
		Correlates       bool
		RowIdx           int
	}
	rowOf := map[string]int{}
	for i, row := range l.rows {
		if _, ok := rowOf[row.MsgID]; !ok {
			rowOf[row.MsgID] = i
		}
	}
	var out []src
	for i, s := range pv.Sources {
		v := src{Source: s, Origin: s.InstructionRecord.Source, Label: view.SourceLabel(s), KindLabel: view.KindLabel(s.Kind),
			Matches: pv.Matches[i], Correlates: view.Correlates(s.Kind), RowIdx: -1}
		if ri, ok := rowOf[s.MsgID]; ok && s.MsgID != "" {
			v.RowIdx = ri
		}
		for _, m := range v.Matches {
			if m.Evidence == model.Strong {
				v.Strong++
			} else {
				v.Possible++
			}
		}
		out = append(out, v)
	}
	s.render(w, "instructions", struct {
		SessionID, Version, Disclaimer string
		Sources                        []src
	}{l.sum.ID, l.sum.Version, provenance.Disclaimer, out})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	l, err := s.session(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rep := usage.Build(l.msgs)
	s.render(w, "usage", struct {
		Sum    model.SessionSummary
		Report usage.Report
		Tables []struct {
			Title string
			Lines []usage.Line
		}
	}{l.sum, rep, []struct {
		Title string
		Lines []usage.Line
	}{{"By model", rep.ByModel}, {"By provider", rep.ByProvider}, {"By agent", rep.ByAgent}}})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	l, err := s.session(r.Context(), r.PathValue("session"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st := view.ComputeStats(l.rows, l.msgs)
	type fileRow struct {
		view.FileEntry
		Rel    string
		RowIdx int
	}
	var out []fileRow
	for _, f := range st.Files {
		fr := fileRow{FileEntry: f, Rel: view.RelPath(l.sum.Directory, f.Path), RowIdx: -1}
		for i, row := range l.rows {
			if row.Kind == trace.KFileChange && row.MsgID == f.MsgID && row.Item == f.Item {
				fr.RowIdx = i
				break
			}
		}
		out = append(out, fr)
	}
	s.render(w, "files", struct {
		SessionID string
		Files     []fileRow
	}{l.sum.ID, out})
}

// handleDeepSearch redirects deep in-session search into the trace view.
func (s *Server) handleDeepSearch(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	v.Set("deep", "1")
	http.Redirect(w, r, "/s/"+url.PathEscape(r.PathValue("session"))+"/trace?"+v.Encode(), http.StatusFound)
}
