package provenance

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
)

// Disclaimer is shown wherever correlations are displayed.
const Disclaimer = "Correlation is textual overlap, not proof the model followed the instruction."

// Match is a heuristic link between an instruction and a later action.
type Match struct {
	MsgID      string
	Index      int
	Seq        int64
	Kind, Tool string
	Evidence   model.Evidence // Strong or Possible only
	Rule       string
	Instr      string // matched instruction snippet
	Action     string // matched action snippet
}

const (
	minSpan        = 40 // normalized characters for a verbatim span (Strong)
	minLiteral     = 6  // characters for a distinctive literal
	skillWindow    = 5  // steps after a skill load considered for Possible
	maxMatchesEach = 50
)

var backtick = regexp.MustCompile("`([^`\n]{2,200})`")

// stop literals are too common to be evidence.
var stop = map[string]bool{
	"true": true, "false": true, "null": true, "string": true, "number": true, "object": true,
	"package.json": true, "readme.md": true, "agents.md": true, "src/": true, "main": true,
	"import": true, "export": true, "return": true, "function": true, "const": true,
}

// Literals returns distinctive backticked tokens from instruction text.
func Literals(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range backtick.FindAllStringSubmatch(text, -1) {
		lit := strings.TrimSpace(m[1])
		low := strings.ToLower(lit)
		if len(lit) < minLiteral || stop[low] || seen[low] || !distinctive(lit) {
			continue
		}
		seen[low] = true
		out = append(out, lit)
	}
	return out
}

// distinctive requires a literal to look like a command, path, or identifier
// rather than a plain English word.
func distinctive(s string) bool {
	if strings.ContainsAny(s, " /.-_:()=@") {
		return true
	}
	upper := 0
	for _, r := range s {
		if unicode.IsUpper(r) || unicode.IsDigit(r) {
			upper++
		}
	}
	return upper > 0 && upper < len(s)
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// spans splits instruction text into normalized lines/sentences of at least minSpan characters.
func spans(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "-*#>0123456789. ")
		n := strings.TrimFunc(normalize(line), func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
		if len(n) >= minSpan {
			out = append(out, n)
		}
	}
	return out
}

// Correlate compares one instruction source against later step texts. It is
// pure and deterministic; ctx is checked between steps so callers can cancel.
func Correlate(ctx context.Context, src Source, steps []store.StepText) ([]Match, error) {
	if src.Text == "" && src.Name == "" && src.Path == "" {
		return nil, nil
	}
	lits := Literals(src.Text)
	sps := spans(src.Text)
	names := mentionNames(src)
	var out []Match
	stepsSinceLoad := map[string]bool{}
	for _, st := range steps {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if st.Seq <= src.Seq {
			continue
		}
		stepsSinceLoad[st.MsgID] = true
		if len(out) >= maxMatchesEach {
			break
		}
		norm := normalize(st.Text)
		if norm == "" {
			continue
		}
		base := Match{MsgID: st.MsgID, Index: st.Index, Seq: st.Seq, Kind: st.Kind, Tool: st.Tool}

		// Strong: verbatim span.
		if m, ok := spanMatch(base, sps, norm); ok {
			out = append(out, m)
			continue
		}
		// Strong: exact distinctive literal inside a tool input.
		hits := literalHits(lits, st.Text)
		if st.Kind == "tool" && len(hits) > 0 {
			m := base
			m.Evidence, m.Rule = model.Strong, "instruction literal used verbatim in tool input"
			m.Instr, m.Action = "`"+hits[0]+"`", around(st.Text, hits[0])
			out = append(out, m)
			continue
		}
		// Possible: ≥2 literals in text/reasoning.
		if len(hits) >= 2 {
			m := base
			m.Evidence, m.Rule = model.Possible, "multiple instruction literals mentioned"
			m.Instr, m.Action = "`"+strings.Join(hits, "`, `")+"`", around(st.Text, hits[0])
			out = append(out, m)
			continue
		}
		// Possible: names the instruction file or skill.
		if n, ok := mentions(norm, names); ok {
			m := base
			m.Evidence, m.Rule = model.Possible, "step names the instruction source"
			m.Instr, m.Action = n, around(st.Text, n)
			out = append(out, m)
			continue
		}
		// Possible: tool shortly after a skill load uses one skill literal.
		if src.Kind == "skill" && st.Kind == "tool" && len(hits) == 1 && len(stepsSinceLoad) <= skillWindow {
			m := base
			m.Evidence, m.Rule = model.Possible, "tool shortly after skill load uses a skill literal"
			m.Instr, m.Action = "`"+hits[0]+"`", around(st.Text, hits[0])
			out = append(out, m)
		}
	}
	return out, nil
}

func spanMatch(base Match, sps []string, norm string) (Match, bool) {
	for _, sp := range sps {
		if strings.Contains(norm, sp) {
			m := base
			m.Evidence, m.Rule = model.Strong, "verbatim instruction span (≥40 chars)"
			m.Instr, m.Action = sp, sp
			return m, true
		}
		// Also accept any 40-char window of a longer line.
		if len(sp) > minSpan {
			for i := 0; i+minSpan <= len(sp); i += minSpan / 2 {
				w := sp[i : i+minSpan]
				if strings.Contains(norm, w) {
					m := base
					m.Evidence, m.Rule = model.Strong, "verbatim instruction span (≥40 chars)"
					m.Instr, m.Action = w, w
					return m, true
				}
			}
		}
	}
	return Match{}, false
}

func literalHits(lits []string, text string) []string {
	var hits []string
	for _, l := range lits {
		if strings.Contains(text, l) {
			hits = append(hits, l)
		}
	}
	return hits
}

func mentionNames(src Source) []string {
	var out []string
	if src.Name != "" && len(src.Name) >= 4 {
		out = append(out, strings.ToLower(src.Name))
	}
	for _, p := range strings.Split(src.Path, ", ") {
		if p == "" {
			continue
		}
		dir := filepath.Base(filepath.Dir(p))
		out = append(out, strings.ToLower(filepath.Join(dir, filepath.Base(p))))
	}
	return out
}

func mentions(norm string, names []string) (string, bool) {
	for _, n := range names {
		if strings.Contains(norm, n) {
			return n, true
		}
	}
	return "", false
}

func around(text, needle string) string {
	i := strings.Index(strings.ToLower(text), strings.ToLower(needle))
	if i < 0 {
		return preview(text)
	}
	lo, hi := max(0, i-50), min(len(text), i+len(needle)+50)
	return preview(text[lo:hi])
}
