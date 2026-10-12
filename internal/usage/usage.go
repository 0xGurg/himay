// Package usage aggregates per-step token usage and cost.
package usage

import (
	"sort"

	"github.com/0xGurg/himay/internal/model"
)

// Line is the usage for one grouping key.
type Line struct {
	Key       string
	Steps     int
	Tokens    model.Tokens
	Cost      float64
	CostKnown bool
	Errors    int
}

// Report groups assistant steps by model, provider, and agent.
type Report struct {
	Total      Line
	ByModel    []Line
	ByProvider []Line
	ByAgent    []Line
	Unknown    int // steps with no model recorded
}

// Build aggregates assistant messages. Steps with no recorded model are
// grouped under "—" and counted in Unknown; nothing is attributed to the
// session-level default model.
func Build(msgs []model.Message) Report {
	byModel, byProv, byAgent := map[string]*Line{}, map[string]*Line{}, map[string]*Line{}
	var rep Report
	rep.Total.Key = "total"
	add := func(m map[string]*Line, k string, msg model.Message) {
		l := m[k]
		if l == nil {
			l = &Line{Key: k}
			m[k] = l
		}
		accumulate(l, msg)
	}
	for _, msg := range msgs {
		if msg.Type != "assistant" || msg.Malformed {
			continue
		}
		accumulate(&rep.Total, msg)
		if !msg.Model.Known() {
			rep.Unknown++
		}
		mk := msg.Model.ID
		if mk == "" {
			mk = "—"
		} else if msg.Model.ProviderID != "" {
			mk = msg.Model.ProviderID + "/" + mk
		}
		add(byModel, mk, msg)
		add(byProv, orDash(msg.Model.ProviderID), msg)
		add(byAgent, orDash(msg.Agent), msg)
	}
	rep.ByModel, rep.ByProvider, rep.ByAgent = sorted(byModel), sorted(byProv), sorted(byAgent)
	return rep
}

func accumulate(l *Line, m model.Message) {
	l.Steps++
	l.Tokens.Add(m.Tokens)
	if m.Cost != nil {
		l.Cost += *m.Cost
		l.CostKnown = true
	}
	if m.Err != nil {
		l.Errors++
	}
}

func sorted(m map[string]*Line) []Line {
	out := make([]Line, 0, len(m))
	for _, l := range m {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens.Total() != out[j].Tokens.Total() {
			return out[i].Tokens.Total() > out[j].Tokens.Total()
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
