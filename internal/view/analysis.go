package view

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
)

// FilterGroup is a named set of row kinds.
type FilterGroup struct {
	Key, Name string
	Kinds     []trace.Kind
}

var FilterGroups = []FilterGroup{
	{"user", "user requests", []trace.Kind{trace.KUser}},
	{"step", "steps", []trace.Kind{trace.KStep}},
	{"text", "text", []trace.Kind{trace.KText}},
	{"reasoning", "reasoning", []trace.Kind{trace.KReasoning, trace.KReasoningUnavailable}},
	{"tool", "tools", []trace.Kind{trace.KTool, trace.KShell}},
	{"subagent", "subagents", []trace.Kind{trace.KSubagent}},
	{"file", "file changes", []trace.Kind{trace.KFileChange}},
	{"error", "errors & retries", []trace.Kind{trace.KError, trace.KRetry}},
	{"switch", "switches", []trace.Kind{trace.KModelSwitched, trace.KAgentSwitched, trace.KLocationSwitched}},
	{"instr", "instructions & skills", []trace.Kind{trace.KInstructionLoaded, trace.KInstructionUpdated, trace.KSkill, trace.KToolsChanged}},
	{"system", "system / synthetic", []trace.Kind{trace.KSystem, trace.KSynthetic}},
	{"lifecycle", "compaction & idle", []trace.Kind{trace.KCompaction, trace.KIdle}},
	{"unknown", "unknown", []trace.Kind{trace.KUnknown}},
}

// Filter selects rows by kind groups (OR), failure, agent, and model (AND).
type Filter struct {
	Groups     map[string]bool
	FailedOnly bool
	Agent      string
	Model      string
}

func (f Filter) Active() bool {
	return len(f.Groups) > 0 || f.FailedOnly || f.Agent != "" || f.Model != ""
}

func (f Filter) Match(r trace.Row) bool {
	if f.FailedOnly && r.Status != trace.SFail && r.Kind != trace.KError {
		return false
	}
	if f.Agent != "" && r.Agent != f.Agent {
		return false
	}
	if f.Model != "" && r.Model.ID != f.Model {
		return false
	}
	if len(f.Groups) == 0 {
		return true
	}
	for _, g := range FilterGroups {
		if !f.Groups[g.Key] {
			continue
		}
		for _, k := range g.Kinds {
			if r.Kind == k {
				return true
			}
		}
	}
	return false
}

// Stats summarizes a trace for the session header.
type Stats struct {
	Tools, Errors int
	Files         []FileEntry
	Agents        []string
	Models        []string
}

// FileEntry is one changed file and its first change.
type FileEntry struct {
	Path  string
	Count int
	MsgID string
	Item  int
	Time  model.StepTime
}

func ComputeStats(rows []trace.Row, msgs []model.Message) Stats {
	var s Stats
	idx := map[string]int{}
	for _, r := range rows {
		switch r.Kind {
		case trace.KError:
			s.Errors++
		case trace.KTool, trace.KSubagent, trace.KSkill, trace.KShell:
			if r.Item >= 0 {
				s.Tools++
			}
			if r.Status == trace.SFail {
				s.Errors++
			}
		case trace.KFileChange:
			for _, f := range r.Files {
				if i, ok := idx[f]; ok {
					s.Files[i].Count++
					continue
				}
				idx[f] = len(s.Files)
				s.Files = append(s.Files, FileEntry{Path: f, Count: 1, MsgID: r.MsgID, Item: r.Item})
			}
		}
	}
	agents, models := map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		if m.Type == "assistant" {
			if m.Agent != "" {
				agents[m.Agent] = true
			}
			if m.Model.ID != "" {
				models[m.Model.ID] = true
			}
		}
	}
	s.Agents, s.Models = sortedKeys(agents), sortedKeys(models)
	return s
}

// RelPath shortens p relative to dir when p is inside it.
func RelPath(dir, p string) string {
	if rel, err := filepath.Rel(dir, p); err == nil && rel != "." && !startsDotDot(rel) {
		return rel
	}
	return p
}

func startsDotDot(p string) bool { return len(p) >= 2 && p[:2] == ".." }

// Provenance is the full instruction analysis for a session.
type Provenance struct {
	Sources []provenance.Source
	Matches [][]provenance.Match
}

// noisyKinds are recorded but not worth correlating.
var noisyKinds = map[string]bool{"date": true, "env": true, "tools": true}

func Correlates(kind string) bool { return !noisyKinds[kind] }

// AnalyzeProvenance extracts recorded instructions and correlates each with
// later actions. msgs must be the complete session.
func AnalyzeProvenance(ctx context.Context, st *store.Store, sessionID string, msgs []model.Message) (*Provenance, error) {
	srcs, err := provenance.Extract(ctx, st, sessionID, msgs)
	if err != nil {
		return nil, err
	}
	steps, err := st.StepTexts(ctx, sessionID, -1)
	if err != nil {
		return nil, err
	}
	p := &Provenance{Sources: srcs, Matches: make([][]provenance.Match, len(srcs))}
	for i := range srcs {
		if err := provenance.Load(ctx, st, &srcs[i]); err != nil {
			return nil, err
		}
		if !Correlates(srcs[i].Kind) {
			continue
		}
		ms, err := provenance.Correlate(ctx, srcs[i], steps)
		if err != nil {
			return nil, err
		}
		p.Matches[i] = ms
	}
	p.sort()
	return p, nil
}

var kindOrder = map[string]int{"agents_md": 0, "agents_md_update": 1, "skill": 2, "skill_mention": 3, "agent_mention": 4,
	"instruction_file_action": 5, "tools": 6, "skill_list": 7, "tool_catalog": 8, "mcp": 9, "reference": 10, "env": 11, "date": 12}

func (p *Provenance) sort() {
	idx := make([]int, len(p.Sources))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool {
		a, b := p.Sources[idx[x]], p.Sources[idx[y]]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		return a.Seq < b.Seq
	})
	srcs := make([]provenance.Source, len(idx))
	ms := make([][]provenance.Match, len(idx))
	for i, j := range idx {
		srcs[i], ms[i] = p.Sources[j], p.Matches[j]
	}
	p.Sources, p.Matches = srcs, ms
}

// SourceLabel picks the most identifying name for a source.
func SourceLabel(s provenance.Source) string {
	for _, v := range []string{s.Name, s.Path, s.Key, s.Preview} {
		if v != "" {
			if s.Removed {
				return v + " (removed)"
			}
			return v
		}
	}
	return "—"
}

func KindLabel(k string) string {
	switch k {
	case "agents_md":
		return "AGENTS.md / instructions"
	case "agents_md_update":
		return "instructions updated"
	case "skill":
		return "skill loaded"
	case "skill_mention":
		return "skill mentioned"
	case "agent_mention":
		return "@agent mentioned"
	case "instruction_file_action":
		return "instruction file touched"
	case "tools":
		return "tools changed"
	case "skill_list":
		return "available skills"
	case "tool_catalog":
		return "tool catalog"
	case "env":
		return "environment"
	case "mcp":
		return "MCP guidance"
	}
	return k
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
